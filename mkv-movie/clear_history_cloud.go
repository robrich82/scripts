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
// answered true, or answered without saying and the last read reached past
// it; one whose removal failed with an error, or wasn't tried, stays failed.
//
// A history read short would pass for removals that took, so a read after the
// deletes must still list every item of the first read that wasn't to be
// removed, or wasn't tried (the anchors); otherwise the deletes are unchecked.
// The last read reached past an item when it lists anything after it in the
// first read. An item it didn't reach past may be where it stopped, so only a
// true reply counts it deleted.
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
			fmt.Fprintf(d.out, "The second read of plex.tv can't be used (%v), so the first read stands.\n", err)
		} else {
			after, listed = again, againListed
		}
	}
	// reached: ids with something after them in the first read that the last
	// read lists. Only the last read counts: an earlier one may have skipped
	// an item while removals were landing.
	reached := make(map[string]bool)
	for i, seen := len(p.cloudRead)-1, false; i >= 0; i-- {
		reached[p.cloudRead[i].ID] = seen
		seen = seen || listed[p.cloudRead[i].ID]
	}

	var check cloudCheck
	var claimed, unconfirmed []cloudEntry
	recovered, unreached, byReply := 0, 0, 0
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
		case failed[e.ID] && !res.unconfirmed[e.ID]:
			check.failed = append(check.failed, e.ID) // an error isn't overruled
		case reached[e.ID] && res.unconfirmed[e.ID]:
			recovered++
		case reached[e.ID]:
		case res.unconfirmed[e.ID]:
			check.failed = append(check.failed, e.ID)
			unreached++
		default:
			byReply++
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
		fmt.Fprintf(d.out, "The last read of plex.tv lists nothing after %d of the items whose removal it didn't confirm, so it can't show they went: they count as not deleted.\n", unreached)
	}
	if byReply > 0 {
		fmt.Fprintf(d.out, "The last read of plex.tv lists nothing after %d of the items it said it removed, so those count as deleted by its replies.\n", byReply)
	}
	// Planned with the first read's items too, so a movie kept beside the
	// target's year is still kept once that year's plays are gone. The
	// re-read's come first, so its titles and years win for items in both.
	inAfter := make(map[string]bool, len(after))
	for _, e := range after {
		inAfter[e.ID] = true
	}
	both := append(slices.Clip(after), slices.DeleteFunc(slices.Clone(p.cloudRead), func(e cloudEntry) bool { return inAfter[e.ID] })...)
	for _, e := range planCloud(both, targets).matched {
		if !matched[e.ID] {
			check.added++
		}
	}
	if check.added > 0 {
		fmt.Fprintf(d.out, "plex.tv now lists %d play(s) of these movies that the plan didn't have; run again to clear them.\n", check.added)
	}
	return check
}

// cloudMovie is a movie in the plex.tv history.
type cloudMovie struct {
	plexID string // plex.tv's id; "" when its items had none
	title  string // without a "(Year)"; "" when no item gave one
	year   int
	key    string // historyTitleKey of title
}

func (m cloudMovie) String() string {
	if m.year == 0 {
		return m.title
	}
	return fmt.Sprintf("%s (%d)", m.title, m.year)
}

// cloudMovieOf is the movie one item names, with the year from a "(Year)" in
// its title when plex.tv gives none.
func cloudMovieOf(e cloudEntry) cloudMovie {
	title, year := splitTitleYear(e.Title)
	return cloudMovie{plexID: e.MovieID, title: title, year: cmp.Or(e.Year, year), key: historyTitleKey(title)}
}

// cloudMovieID is what groups e with its movie's other plays: plex.tv's id,
// or the title and year for an item without one. "" means e is no movie, or
// names none.
func cloudMovieID(e cloudEntry) string {
	switch {
	case e.Type != cloudTypeMovie:
		return ""
	case e.MovieID != "":
		return e.MovieID
	case e.Title == "":
		return ""
	default:
		return "\x00" + e.Title + "\x00" + strconv.Itoa(e.Year)
	}
}

// cloudMoviesOf gathers the movies of entries by cloudMovieID. A movie's
// title and year are the first its items give.
func cloudMoviesOf(entries []cloudEntry) map[string]*cloudMovie {
	movies := make(map[string]*cloudMovie)
	for _, e := range entries {
		id := cloudMovieID(e)
		if id == "" {
			continue
		}
		item := cloudMovieOf(e)
		m := movies[id]
		if m == nil {
			movies[id] = &item
			continue
		}
		if m.title == "" {
			m.title, m.key = item.title, item.key
		}
		if m.year == 0 {
			m.year = item.year
		}
	}
	return movies
}

// cloudSummary is a movie's matched plex.tv plays, and the one watched last.
type cloudSummary struct {
	movie cloudMovie
	plays int
	last  cloudEntry
}

// cloudPlan is what a run would delete from plex.tv, and what it left alone.
type cloudPlan struct {
	matched       []cloudEntry
	movies        map[string]*cloudSummary // by cloudMovieID
	otherYears    []string                 // movies with a target's title and a year more than one from its
	yearless      []string                 // movies with a target's title and no year, beside one in the target's range
	yearUnchecked []string                 // targets that matched a movie with no year
	severalYears  []string                 // dated targets whose range took movies of more than one year
	missing       []string                 // targets that matched nothing
	titleless     int                      // movie items naming no movie, or none with a title, and items plex.tv sent none for
}

// planCloud picks the plex.tv plays of the target movies. A target's year
// must be within one of the movie's, as on the server, and a movie with
// another year is listed, not deleted. A movie plex.tv gives no year matches
// on title alone, unless the title is there with a year in the target's
// range: it's then another movie, listed and kept. Plays are grouped by
// plex.tv's movie id, so a play without a year goes with its movie's others.
func planCloud(entries []cloudEntry, targets []historyTarget) cloudPlan {
	movies := cloudMoviesOf(entries)
	plan := cloudPlan{movies: make(map[string]*cloudSummary)}
	take, otherYear, yearless := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	listed := make(map[string]bool)
	for _, t := range targets {
		if listed[t.raw] {
			continue
		}
		listed[t.raw] = true
		inRange := func(year int) bool { return abs(year-t.year) <= 1 }
		exactYear := false
		if t.year != 0 {
			for _, m := range movies {
				if m.key == t.key && m.year != 0 && inRange(m.year) {
					exactYear = true
				}
			}
		}
		found, unchecked := false, false
		years := make(map[int]bool) // of the movies taken for being in range
		for id, m := range movies {
			if m.key != t.key {
				continue
			}
			switch {
			case t.year == 0:
			case m.year != 0 && inRange(m.year):
				years[m.year] = true
			case m.year != 0:
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
		if len(years) > 1 {
			plan.severalYears = append(plan.severalYears, t.raw)
		}
	}
	for _, e := range entries {
		id := cloudMovieID(e)
		if id == "" || movies[id].title == "" {
			if e.Type == cloudTypeMovie || e.Type == "" { // "": plex.tv sent no item
				plan.titleless++
			}
			continue
		}
		if !take[id] {
			continue
		}
		plan.matched = append(plan.matched, e)
		sum := plan.movies[id]
		if sum == nil {
			sum = &cloudSummary{movie: *movies[id], last: e}
			plan.movies[id] = sum
		}
		sum.plays++
		if cloudTime(e).After(cloudTime(sum.last)) {
			sum.last = e
		}
	}
	plan.otherYears = untaken(plan.movies, movies, otherYear)
	plan.yearless = untaken(plan.movies, movies, yearless)
	return plan
}

// untaken names the movies of ids that no target took, sorted.
func untaken(taken map[string]*cloudSummary, movies map[string]*cloudMovie, ids map[string]bool) []string {
	var names []string
	for id := range ids {
		if taken[id] == nil {
			names = append(names, movies[id].String())
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

// printCloudPlan lists each matched plex.tv movie with its play count and
// when it was last watched, then what was flagged or left alone and the
// targets not found. owner is the plex.tv user whose history it is.
func printCloudPlan(out io.Writer, owner string, plan cloudPlan) {
	fmt.Fprintf(out, "Watch history for %s on plex.tv:\n", owner)
	sums := slices.SortedFunc(maps.Values(plan.movies), func(a, b *cloudSummary) int {
		return cmp.Or(strings.Compare(a.movie.title, b.movie.title), cmp.Compare(a.movie.year, b.movie.year),
			strings.Compare(a.movie.plexID, b.movie.plexID))
	})
	// As in printHistoryPlan, titles are compared as matching folds them.
	perName, folded := make(map[string]int), make(map[string]bool)
	for _, sum := range sums {
		perName[sum.movie.String()]++
		folded[sum.movie.key+"\x00"+strconv.Itoa(sum.movie.year)] = true
	}
	for _, sum := range sums {
		name := sum.movie.String()
		if perName[name] > 1 {
			name += " [" + cmp.Or(sum.movie.plexID, "no id") + "]" // nothing else tells them apart
		}
		fmt.Fprintf(out, "  %-40s %d play(s)%s\n", name, sum.plays, describeCloudItem(sum.last))
	}
	if len(folded) < len(sums) {
		fmt.Fprintln(out, "Titles that repeat, or that matching counts as the same, may be different movies: check each before answering.")
	}
	if len(plan.severalYears) > 0 {
		fmt.Fprintf(out, "These matched plex.tv movies of more than one year, which may be different movies: check each before answering: %s\n", strings.Join(plan.severalYears, ", "))
	}
	if len(plan.otherYears) > 0 {
		fmt.Fprintf(out, "Not deleting these, which have another year: %s\n", strings.Join(plan.otherYears, ", "))
	}
	if len(plan.yearless) > 0 {
		fmt.Fprintf(out, "Not deleting these, which plex.tv gives no year while the title is there within a year of the one asked for: %s\n", strings.Join(plan.yearless, ", "))
	}
	if len(plan.yearUnchecked) > 0 {
		fmt.Fprintf(out, "plex.tv records no year for a movie these matched, so it matched on title alone: %s\n", strings.Join(plan.yearUnchecked, ", "))
	}
	if plan.titleless > 0 {
		fmt.Fprintf(out, "plex.tv lists %d item(s) that name no movie to match, so they're left alone.\n", plan.titleless)
	}
	if len(plan.missing) > 0 {
		fmt.Fprintln(out, "Not in plex.tv history:")
		for _, m := range plan.missing {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
}

// describeCloudItem is ", last watched <date>", in local time like the
// server's plan, or "" when plex.tv's date can't be read.
func describeCloudItem(e cloudEntry) string {
	t := cloudTime(e)
	if t.IsZero() {
		return ""
	}
	return ", last watched " + t.Local().Format(time.DateOnly)
}

// describe names e with its movie as the plan grouped it, so an item without
// its movie's title still has it.
func (p cloudPlan) describe(e cloudEntry) string {
	movie := cloudMovieOf(e)
	if sum := p.movies[cloudMovieID(e)]; sum != nil {
		movie = sum.movie
	}
	return "plex.tv " + movie.String() + describeCloudItem(e)
}
