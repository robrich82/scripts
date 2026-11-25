package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// plexTVAccount is the plex.tv user whose history goes with acct's: the
// PLEX_TOKEN owner, when acct has the owner's name exactly. Otherwise skip
// says why plex.tv is left alone: acct is someone else, or plex.tv refuses
// the token (see tokenRefused). Any other failure is err, so a brief plex.tv
// outage can't leave its history behind unnoticed.
func plexTVAccount(owner OwnerLookup, acct plexAccount) (user plexTVUser, skip string, err error) {
	user, err = owner.User()
	switch refused := tokenRefused(err); {
	case refused != nil:
		return plexTVUser{}, fmt.Sprintf("plex.tv refused this PLEX_TOKEN (%s), so plex.tv history isn't cleared.", refused.Status), nil
	case err != nil:
		return plexTVUser{}, "", fmt.Errorf("finding the PLEX_TOKEN owner: %w", err)
	case strings.EqualFold(acct.Name, user.Username) && acct.Name != user.Username:
		return plexTVUser{}, fmt.Sprintf("The server account %s isn't named exactly as the PLEX_TOKEN owner %s, so plex.tv history isn't cleared.", acct.Name, user.Username), nil
	case acct.Name != user.Username:
		return plexTVUser{}, fmt.Sprintf("This PLEX_TOKEN can clear only its owner's (%s) plex.tv history, so any %s has there is left alone.", user.Username, acct.Name), nil
	case user.UUID == "":
		return plexTVUser{}, "", errors.New("plex.tv gave no user id for the PLEX_TOKEN owner")
	}
	return user, "", nil
}

// readCloudHistory reads every item of user uuid's plex.tv watch history, in
// plex.tv's order, each once. retry runs each page's request, waiting out
// rate limits.
func readCloudHistory(c CloudHistoryClient, uuid string, retry func(func() error) error) ([]cloudEntry, error) {
	var entries []cloudEntry
	seenCursor, seenID := make(map[string]bool), make(map[string]bool)
	after := ""
	for {
		var page cloudPage
		err := retry(func() (err error) {
			page, err = c.CloudHistoryPage(uuid, after)
			return err
		})
		if err != nil {
			return nil, err
		}
		added := 0
		for _, e := range page.Entries {
			if !seenID[e.ID] {
				seenID[e.ID] = true
				entries = append(entries, e)
				added++
			}
		}
		if !page.HasNext {
			return entries, nil
		}
		// Without progress the paging would loop forever, or stop early and
		// read part of the history as all of it.
		if page.Cursor == "" || seenCursor[page.Cursor] || added == 0 {
			return nil, errors.New("plex.tv: the watch history's paging doesn't move on (a missing or repeated cursor, or a page with nothing new), so it can't be read whole")
		}
		seenCursor[page.Cursor] = true
		after = page.Cursor
	}
}

// settleWait is how long to wait before reading the plex.tv history a second
// time, when the first read after the deletes still lists removed items:
// plex.tv may not show a removal at once.
const settleWait = 10 * time.Second

// cloudCheck is what re-reading plex.tv after the deletes found.
type cloudCheck struct {
	failed    []string // matched ids not deleted, by the history where it can tell, else by the reply
	unchecked string   // why the history couldn't be read again, or trusted; failed is then the replies'
	added     int      // matching items the plan didn't have, for another run
}

// checkCloudRemoved re-reads the plex.tv history after the deletes to see
// which removals took, since its history, not its replies, says so. An item
// it still lists isn't deleted. One it no longer lists is deleted when plex.tv
// answered anything but an error, as long as the re-read reached past it;
// one whose removal failed with an error, or wasn't tried, stays failed.
//
// A history read short would pass for removals that took, so the re-read must
// still list every item of the first read that wasn't to be removed, or
// wasn't tried (the anchors); otherwise the deletes are unchecked. An item
// with no anchor after it in the first read may be past where the re-read
// stopped, so only its reply counts.
func checkCloudRemoved(d clearHistoryDeps, retry func(func() error) error, p clearPlan, targets []historyTarget, res deleteResult) cloudCheck {
	matched := make(map[string]bool, len(p.cloud.matched))
	for _, e := range p.cloud.matched {
		matched[e.ID] = true
	}
	failed := make(map[string]bool, len(res.failed))
	for _, id := range res.failed {
		failed[id] = true
	}
	anchor := func(id string) bool { return !matched[id] || res.untried[id] }
	reached := make(map[string]bool) // ids with an anchor after them in the first read
	for i, seen := len(p.cloudRead)-1, false; i >= 0; i-- {
		reached[p.cloudRead[i].ID] = seen
		seen = seen || anchor(p.cloudRead[i].ID)
	}
	read := func() ([]cloudEntry, map[string]bool, error) {
		after, err := readCloudHistory(d.cloud, p.owner.UUID, retry)
		if err != nil {
			return nil, nil, err
		}
		listed := make(map[string]bool, len(after))
		for _, e := range after {
			listed[e.ID] = true
		}
		lost := 0
		for _, e := range p.cloudRead {
			if anchor(e.ID) && !listed[e.ID] {
				lost++
			}
		}
		if lost > 0 {
			return nil, nil, fmt.Errorf("the history read after the deletes lacks %d item(s) that weren't removed, so it can't be trusted", lost)
		}
		return after, listed, nil
	}
	// pending counts the listed items whose removal may have taken.
	pending := func(listed map[string]bool) int {
		n := 0
		for id := range matched {
			if listed[id] && (!failed[id] || res.unconfirmed[id]) {
				n++
			}
		}
		return n
	}

	after, listed, err := read()
	if err != nil {
		fmt.Fprintf(d.out, "Couldn't check the plex.tv deletes: %v\n", err)
		return cloudCheck{failed: res.failed, unchecked: err.Error()}
	}
	if n := pending(listed); n > 0 {
		fmt.Fprintf(d.out, "plex.tv still lists %d item(s) it was asked to remove; checking again in %s.\n", n, settleWait)
		d.sleep(settleWait)
		if again, againListed, err := read(); err != nil {
			fmt.Fprintf(d.out, "Couldn't read plex.tv again (%v), so the first read stands.\n", err)
		} else {
			after, listed = again, againListed
		}
	}

	var check cloudCheck
	var claimed, unconfirmed []cloudEntry
	recovered, unreached := 0, 0
	for _, e := range p.cloud.matched {
		switch {
		case listed[e.ID]:
			check.failed = append(check.failed, e.ID)
			switch {
			case !failed[e.ID]:
				claimed = append(claimed, e)
			case res.unconfirmed[e.ID]:
				unconfirmed = append(unconfirmed, e)
			}
		case res.unconfirmed[e.ID] && !reached[e.ID]:
			check.failed = append(check.failed, e.ID)
			unreached++
		case res.unconfirmed[e.ID]:
			recovered++
		case failed[e.ID]:
			check.failed = append(check.failed, e.ID) // an error isn't overruled
		}
	}
	printStillListed := func(what string, entries []cloudEntry) {
		if len(entries) == 0 {
			return
		}
		fmt.Fprintf(d.out, "plex.tv still lists %d of the items %s:\n", len(entries), what)
		for _, e := range entries {
			fmt.Fprintf(d.out, "  still listed: %s\n", p.cloud.describe(e))
		}
	}
	printStillListed("it said it removed", claimed)
	printStillListed("whose removal it didn't confirm", unconfirmed)
	if recovered > 0 {
		fmt.Fprintf(d.out, "plex.tv no longer lists %d of the items whose removal it didn't confirm.\n", recovered)
	}
	if unreached > 0 {
		fmt.Fprintf(d.out, "plex.tv lists nothing older than %d of the items whose removal it didn't confirm, so its history can't show they went: they count as not deleted.\n", unreached)
	}
	// Planned with the first read too, so a show kept beside the target's
	// year is still kept once that year's items are gone.
	firstRead := make(map[string]bool, len(p.cloudRead))
	for _, e := range p.cloudRead {
		firstRead[e.ID] = true
	}
	both := append(slices.Clip(p.cloudRead), slices.DeleteFunc(slices.Clone(after), func(e cloudEntry) bool { return firstRead[e.ID] })...)
	for _, e := range planCloud(both, targets).matched {
		if !matched[e.ID] {
			check.added++
		}
	}
	if check.added > 0 {
		fmt.Fprintf(d.out, "plex.tv now lists %d item(s) of these shows that the plan didn't have; run again to clear them.\n", check.added)
	}
	return check
}

// cloudShow is a show in the plex.tv history.
type cloudShow struct {
	plexID string // plex.tv's id; "" when its items had none
	title  string // without a "(Year)"; "" when no item gave one
	year   int
	key    string // historyTitleKey of title
}

func (s cloudShow) String() string {
	if s.year == 0 {
		return s.title
	}
	return fmt.Sprintf("%s (%d)", s.title, s.year)
}

// cloudShowOf is the show one item names, with the year from a "(Year)" in
// its title when plex.tv gives none.
func cloudShowOf(e cloudEntry) cloudShow {
	title, year := splitTitleYear(e.Show)
	return cloudShow{plexID: e.ShowID, title: title, year: cmp.Or(e.Year, year), key: historyTitleKey(title)}
}

// cloudShowID is what groups e with its show's other items: plex.tv's id, or
// the title and year for an item without one. "" means e names no show.
func cloudShowID(e cloudEntry) string {
	switch {
	case e.ShowID != "":
		return e.ShowID
	case e.Show == "":
		return ""
	default:
		return "\x00" + e.Show + "\x00" + strconv.Itoa(e.Year)
	}
}

// cloudShowsOf gathers the shows of entries by cloudShowID. A show's title and
// year are the first its items give.
func cloudShowsOf(entries []cloudEntry) map[string]*cloudShow {
	shows := make(map[string]*cloudShow)
	for _, e := range entries {
		id := cloudShowID(e)
		if id == "" {
			continue
		}
		item := cloudShowOf(e)
		s := shows[id]
		if s == nil {
			shows[id] = &item
			continue
		}
		if s.title == "" {
			s.title, s.key = item.title, item.key
		}
		if s.year == 0 {
			s.year = item.year
		}
	}
	return shows
}

// cloudSummary is a show's matched plex.tv items, and the one watched last.
type cloudSummary struct {
	show             cloudShow
	episodes, others int // EPISODE items, and SEASON and SHOW ones
	last             cloudEntry
}

// cloudPlan is what a run would delete from plex.tv, and what it left alone.
type cloudPlan struct {
	matched       []cloudEntry
	shows         map[string]*cloudSummary // by cloudShowID
	otherYears    []string                 // shows with a target's title and another year
	yearless      []string                 // shows with a target's title and no year, beside one with the target's year
	yearUnchecked []string                 // targets that matched a show with no year
	missing       []string                 // targets that matched nothing
	showless      int                      // episode, season and show items naming no show, or none with a title
}

// planCloud picks the plex.tv items of the target shows: episodes, seasons
// and the shows themselves. A target's year must equal the show's, and a show
// with another year is listed, not deleted. A show plex.tv gives no year
// matches on title alone, unless the title is there with the target's year:
// it's then another show, listed and kept. (The server's link from a
// year-less title to a retitled show is plex.tv's grouping by show id.)
func planCloud(entries []cloudEntry, targets []historyTarget) cloudPlan {
	shows := cloudShowsOf(entries)
	plan := cloudPlan{shows: make(map[string]*cloudSummary)}
	take, otherYear, yearless := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	dated := make(map[string]bool) // the title key and year of each show with a year
	for _, s := range shows {
		if s.year != 0 {
			dated[s.key+"\x00"+strconv.Itoa(s.year)] = true
		}
	}
	listed := make(map[string]bool)
	for _, t := range targets {
		if listed[t.raw] {
			continue
		}
		listed[t.raw] = true
		exactYear := dated[t.key+"\x00"+strconv.Itoa(t.year)]
		found, unchecked := false, false
		for id, s := range shows {
			if s.key != t.key {
				continue
			}
			switch {
			case t.year == 0 || s.year == t.year:
			case s.year != 0:
				otherYear[id] = true
				continue
			case exactYear:
				yearless[id] = true
				continue
			default:
				unchecked = true
			}
			take[id] = true
			found = true
		}
		if !found {
			plan.missing = append(plan.missing, t.raw)
		} else if unchecked {
			plan.yearUnchecked = append(plan.yearUnchecked, t.raw)
		}
	}
	for _, e := range entries {
		id := cloudShowID(e)
		if id == "" || shows[id].title == "" {
			switch e.Type {
			case cloudTypeEpisode, cloudTypeSeason, cloudTypeShow, "": // "": plex.tv sent no item
				plan.showless++
			}
			continue
		}
		if !take[id] {
			continue
		}
		plan.matched = append(plan.matched, e)
		sum := plan.shows[id]
		if sum == nil {
			sum = &cloudSummary{show: *shows[id], last: e}
			plan.shows[id] = sum
		}
		if e.Type == cloudTypeEpisode {
			sum.episodes++
		} else {
			sum.others++
		}
		if cloudTime(e).After(cloudTime(sum.last)) {
			sum.last = e
		}
	}
	plan.otherYears = untaken(plan.shows, shows, otherYear)
	plan.yearless = untaken(plan.shows, shows, yearless)
	return plan
}

// untaken names the shows of ids that no target took, sorted.
func untaken(taken map[string]*cloudSummary, shows map[string]*cloudShow, ids map[string]bool) []string {
	var names []string
	for id := range ids {
		if taken[id] == nil {
			names = append(names, shows[id].String())
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// cloudTime is when e was watched; zero when plex.tv's date can't be read.
func cloudTime(e cloudEntry) time.Time {
	t, _ := time.Parse(time.RFC3339, e.Date)
	return t
}

// printCloudPlan lists each matched plex.tv show with its item counts and the
// item watched last, then what was left alone and the targets not found.
// owner is the plex.tv user whose history it is.
func printCloudPlan(out io.Writer, owner string, plan cloudPlan) {
	fmt.Fprintf(out, "Watch history for %s on plex.tv:\n", owner)
	sums := slices.SortedFunc(maps.Values(plan.shows), func(a, b *cloudSummary) int {
		return cmp.Or(strings.Compare(a.show.title, b.show.title), cmp.Compare(a.show.year, b.show.year),
			strings.Compare(a.show.plexID, b.show.plexID))
	})
	// As in printHistoryPlan, titles are compared as matching folds them.
	perName, folded := make(map[string]int), make(map[string]bool)
	for _, sum := range sums {
		perName[sum.show.String()]++
		folded[sum.show.key+"\x00"+strconv.Itoa(sum.show.year)] = true
	}
	for _, sum := range sums {
		name := sum.show.String()
		if perName[name] > 1 {
			name += " [" + cmp.Or(sum.show.plexID, "no id") + "]" // nothing else tells them apart
		}
		counts := fmt.Sprintf("%d episode(s)", sum.episodes)
		if sum.others > 0 {
			counts += fmt.Sprintf(", %d season or show item(s)", sum.others)
		}
		fmt.Fprintf(out, "  %-40s %s, last watched %s\n", name, counts, describeCloudItem(sum.last))
	}
	if len(folded) < len(sums) {
		fmt.Fprintln(out, "Some of these have the same year and a title matching counts as the same, so they may be different shows: check each before answering.")
	}
	if len(plan.otherYears) > 0 {
		fmt.Fprintf(out, "Not deleting these, which have another year: %s\n", strings.Join(plan.otherYears, ", "))
	}
	if len(plan.yearless) > 0 {
		fmt.Fprintf(out, "Not deleting these, which plex.tv gives no year while the title is there with the year asked for: %s\n", strings.Join(plan.yearless, ", "))
	}
	if len(plan.yearUnchecked) > 0 {
		fmt.Fprintf(out, "plex.tv records no year for a show these matched, so it matched on title alone: %s\n", strings.Join(plan.yearUnchecked, ", "))
	}
	if plan.showless > 0 {
		fmt.Fprintf(out, "plex.tv lists %d item(s) that name no show to match, so they're left alone.\n", plan.showless)
	}
	if len(plan.missing) > 0 {
		fmt.Fprintln(out, "Not in plex.tv history:")
		for _, m := range plan.missing {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
}

// describeCloudItem names an item within its show, with the day, in local
// time like the server's plan, that it was watched.
func describeCloudItem(e cloudEntry) string {
	var s string
	switch e.Type {
	case cloudTypeEpisode:
		s = fmt.Sprintf("S%02dE%02d %q", e.Season, e.Episode, e.Title)
	case cloudTypeSeason:
		s = fmt.Sprintf("season %d", e.Season)
	default:
		s = "the show"
	}
	if t := cloudTime(e); !t.IsZero() {
		s += " on " + t.Local().Format(time.DateOnly)
	}
	return s
}

// describe names e with its show as the plan grouped it, so an item without
// its show's title still has it.
func (p cloudPlan) describe(e cloudEntry) string {
	show := cloudShowOf(e)
	if sum := p.shows[cloudShowID(e)]; sum != nil {
		show = sum.show
	}
	return "plex.tv " + show.String() + " " + describeCloudItem(e)
}
