package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// plexTVAccount is the plex.tv user whose history goes with acct's: the
// PLEX_TOKEN owner, when acct has the owner's name exactly. Otherwise skip
// says why plex.tv is left alone: acct is someone else, or plex.tv refuses
// the token (401 or 403, as for a server's own token). Any other failure is
// err, so a brief plex.tv outage can't leave its history behind unnoticed.
func plexTVAccount(owner OwnerLookup, acct plexAccount) (user plexTVUser, skip string, err error) {
	user, err = owner.User()
	var status *plexTVStatusError
	switch {
	case errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden):
		return plexTVUser{}, fmt.Sprintf("plex.tv refused this PLEX_TOKEN (%s), so plex.tv history isn't cleared.", status.Status), nil
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

// cloudShow is a show in the plex.tv history.
type cloudShow struct {
	plexID string // plex.tv's id; "" when its items had none
	title  string // without a "(Year)"
	year   int
	key    string // historyTitleKey of title
}

func (s cloudShow) String() string {
	if s.year == 0 {
		return s.title
	}
	return fmt.Sprintf("%s (%d)", s.title, s.year)
}

// cloudShowID is what groups e with its show's other items: plex.tv's id, or
// the title and year for an item without one. "" means e is of no show, as a
// movie isn't.
func cloudShowID(e cloudEntry) string {
	switch {
	case e.Show == "":
		return ""
	case e.ShowID != "":
		return e.ShowID
	default:
		return "\x00" + e.Show + "\x00" + strconv.Itoa(e.Year)
	}
}

// cloudShowsOf gathers the shows of entries, by cloudShowID, in the order
// first seen. A show's year is the first its items give, from plex.tv or else
// from a "(Year)" in the title.
func cloudShowsOf(entries []cloudEntry) (map[string]*cloudShow, []string) {
	shows := make(map[string]*cloudShow)
	var order []string
	for _, e := range entries {
		id := cloudShowID(e)
		if id == "" {
			continue
		}
		title, year := splitTitleYear(e.Show)
		s := shows[id]
		if s == nil {
			s = &cloudShow{plexID: e.ShowID, title: title, key: historyTitleKey(title)}
			shows[id] = s
			order = append(order, id)
		}
		if s.year == 0 {
			s.year = cmp.Or(e.Year, year)
		}
	}
	return shows, order
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
	yearUnchecked []string                 // targets that matched a show with no year
	missing       []string                 // targets that matched nothing
}

// planCloud picks the plex.tv items of the target shows: episodes, seasons
// and the shows themselves. plex.tv records a show's year, so a target's
// year must equal it; a show with another year is listed, not deleted. A
// show with no year matches on title alone, unless a show with the target's
// title and year is there too: the year-less one is then another show.
func planCloud(entries []cloudEntry, targets []historyTarget) cloudPlan {
	shows, order := cloudShowsOf(entries)
	plan := cloudPlan{shows: make(map[string]*cloudSummary)}
	take, otherYear := make(map[string]bool), make(map[string]bool)
	listed := make(map[string]bool)
	for _, t := range targets {
		if listed[t.raw] {
			continue
		}
		listed[t.raw] = true
		exactYear := t.year != 0 && slices.ContainsFunc(order, func(id string) bool {
			return shows[id].key == t.key && shows[id].year == t.year
		})
		found, unchecked := false, false
		for _, id := range order {
			s := shows[id]
			if s.key != t.key {
				continue
			}
			switch {
			case t.year == 0 || s.year == t.year:
			case s.year != 0:
				otherYear[id] = true
				continue
			case exactYear:
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
	for id := range otherYear {
		if plan.shows[id] == nil { // another target may have taken it
			plan.otherYears = append(plan.otherYears, shows[id].String())
		}
	}
	slices.Sort(plan.otherYears)
	plan.otherYears = slices.Compact(plan.otherYears)
	return plan
}

// cloudTime is when e was watched; zero when plex.tv's date can't be read.
func cloudTime(e cloudEntry) time.Time {
	t, _ := time.Parse(time.RFC3339, e.Date)
	return t
}

// printCloudPlan lists each matched plex.tv show with its item counts and the
// item watched last, then the shows left for another year, the targets
// matched on title alone and the targets not found. owner is the plex.tv
// user whose history it is.
func printCloudPlan(out io.Writer, owner string, plan cloudPlan) {
	fmt.Fprintf(out, "Watch history for %s on plex.tv:\n", owner)
	sums := slices.SortedFunc(maps.Values(plan.shows), func(a, b *cloudSummary) int {
		return cmp.Or(strings.Compare(a.show.title, b.show.title), cmp.Compare(a.show.year, b.show.year),
			strings.Compare(a.show.plexID, b.show.plexID))
	})
	// As in printHistoryPlan, titles are compared as matching folds them.
	perName, perFolded := make(map[string]int), make(map[string]int)
	for _, sum := range sums {
		perName[sum.show.String()]++
		perFolded[sum.show.key+"\x00"+strconv.Itoa(sum.show.year)]++
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
	if len(perFolded) < len(sums) {
		fmt.Fprintln(out, "Shows with the same year whose titles repeat, or that matching counts as the same, may be different shows: check each before answering.")
	}
	if len(plan.otherYears) > 0 {
		fmt.Fprintf(out, "Not deleting these, which have another year: %s\n", strings.Join(plan.otherYears, ", "))
	}
	if len(plan.yearUnchecked) > 0 {
		fmt.Fprintf(out, "plex.tv records no year for a show these matched, so it matched on title alone: %s\n", strings.Join(plan.yearUnchecked, ", "))
	}
	if len(plan.missing) > 0 {
		fmt.Fprintln(out, "Not in plex.tv history:")
		for _, m := range plan.missing {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
}

// describeCloudItem names an item within its show, with the day it was watched.
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
		s += " on " + t.UTC().Format(time.DateOnly)
	}
	return s
}

func describeCloudEntry(e cloudEntry) string {
	return "plex.tv " + cloudShow{title: e.Show, year: e.Year}.String() + " " + describeCloudItem(e)
}
