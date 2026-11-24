package main

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// plexTVAccount is the plex.tv user whose history goes with acct's, or why
// none does: plex.tv only has the PLEX_TOKEN owner's. account is --account.
func plexTVAccount(server HistoryClient, owner OwnerLookup, acct plexAccount, account string) (uuid, skip string) {
	user, err := owner.User()
	if err != nil {
		return "", fmt.Sprintf("plex.tv: couldn't look up the PLEX_TOKEN owner (%v), so plex.tv history isn't cleared.", err)
	}
	if account != "" {
		ownerAcct, err := resolveHistoryAccount(server, owner, "")
		if err != nil {
			return "", fmt.Sprintf("plex.tv: couldn't find the PLEX_TOKEN owner's server account (%v), so plex.tv history isn't cleared.", err)
		}
		if ownerAcct.ID != acct.ID {
			return "", fmt.Sprintf("plex.tv holds only the PLEX_TOKEN owner's (%s) history, so %s's there isn't cleared.", ownerAcct.Name, acct.Name)
		}
	}
	if user.UUID == "" {
		return "", "plex.tv: the PLEX_TOKEN owner's user has no id, so plex.tv history isn't cleared."
	}
	return user.UUID, ""
}

// cloudShow is a show in the plex.tv history: its title and year.
type cloudShow struct {
	title string
	year  int
}

func (s cloudShow) String() string {
	if s.year == 0 {
		return s.title
	}
	return fmt.Sprintf("%s (%d)", s.title, s.year)
}

// cloudSummary is a show's matched plex.tv items, and the one watched last.
type cloudSummary struct {
	episodes, others int // EPISODE items, and SEASON and SHOW ones
	last             cloudEntry
}

// cloudPlan is what a run would delete from plex.tv, and what it left alone.
type cloudPlan struct {
	matched       []cloudEntry
	shows         map[cloudShow]*cloudSummary
	otherYears    []string // shows with a target's title and another year
	yearUnchecked []string // targets that matched a show with no year
	missing       []string // targets that matched nothing
}

// cloudShowOf is the show a plex.tv item belongs to, with the year from its
// title when plex.tv gives none; ok is false for an item not of a show, such
// as a movie, which has no Show.
func cloudShowOf(e cloudEntry) (s cloudShow, key string, ok bool) {
	if e.Show == "" {
		return cloudShow{}, "", false
	}
	title, year := splitTitleYear(e.Show)
	return cloudShow{e.Show, cmp.Or(e.Year, year)}, historyTitleKey(title), true
}

// planCloud picks the plex.tv items of the target shows: episodes, seasons
// and the shows themselves. plex.tv records a show's year, so a target's
// year must equal it; a show with another year is listed, not deleted.
func planCloud(entries []cloudEntry, targets []historyTarget) cloudPlan {
	plan := cloudPlan{shows: make(map[cloudShow]*cloudSummary)}
	take := make(map[string]bool)
	otherYear := make(map[cloudShow]bool)
	listed := make(map[string]bool)
	for _, t := range targets {
		if listed[t.raw] {
			continue
		}
		listed[t.raw] = true
		found, unchecked := false, false
		for _, e := range entries {
			s, key, ok := cloudShowOf(e)
			if !ok || key != t.key {
				continue
			}
			switch {
			case t.year == 0 || s.year == t.year:
			case s.year != 0:
				otherYear[s] = true
				continue
			default:
				unchecked = true
			}
			take[e.ID] = true
			found = true
		}
		if !found {
			plan.missing = append(plan.missing, t.raw)
		} else if unchecked {
			plan.yearUnchecked = append(plan.yearUnchecked, t.raw)
		}
	}
	for _, e := range entries {
		if !take[e.ID] {
			continue
		}
		s, _, _ := cloudShowOf(e)
		plan.matched = append(plan.matched, e)
		sum := plan.shows[s]
		if sum == nil {
			sum = &cloudSummary{last: e}
			plan.shows[s] = sum
		}
		if e.Type == "EPISODE" {
			sum.episodes++
		} else {
			sum.others++
		}
		if e.Date > sum.last.Date { // RFC 3339 in UTC, so text order is time order
			sum.last = e
		}
	}
	for s := range otherYear {
		if plan.shows[s] == nil { // another target may have taken it
			plan.otherYears = append(plan.otherYears, s.String())
		}
	}
	slices.Sort(plan.otherYears)
	return plan
}

// printCloudPlan lists each matched plex.tv show with its item counts and the
// item watched last, then what was left alone.
func printCloudPlan(out io.Writer, plan cloudPlan) {
	fmt.Fprintln(out, "Watch history on plex.tv:")
	shows := slices.SortedFunc(maps.Keys(plan.shows), func(a, b cloudShow) int {
		return cmp.Or(strings.Compare(a.title, b.title), cmp.Compare(a.year, b.year))
	})
	for _, s := range shows {
		sum := plan.shows[s]
		counts := strconv.Itoa(sum.episodes) + " episode(s)"
		if sum.others > 0 {
			counts += fmt.Sprintf(", %d season or show item(s)", sum.others)
		}
		fmt.Fprintf(out, "  %-40s %s, last watched %s\n", s, counts, describeCloudItem(sum.last))
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
	case "EPISODE":
		s = fmt.Sprintf("S%02dE%02d %q", e.Season, e.Episode, e.Title)
	case "SEASON":
		s = fmt.Sprintf("season %d", e.Season)
	default:
		s = "the show"
	}
	if len(e.Date) >= len("2006-01-02") {
		s += " on " + e.Date[:len("2006-01-02")]
	}
	return s
}

func describeCloudEntry(e cloudEntry) string {
	s, _, _ := cloudShowOf(e)
	return "plex.tv " + s.String() + " " + describeCloudItem(e)
}
