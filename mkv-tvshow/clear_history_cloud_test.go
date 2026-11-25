package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// newCloudRun is newClearHistoryRun with the owner's plex.tv history: Bluey
// (2018) as episodes, a season and the show; Bluey (1992); Peppa Pig with no
// year; and a movie titled Bluey.
func newCloudRun(t *testing.T, answers ...string) *clearHistoryRun {
	r := newClearHistoryRun(t, answers...)
	r.tv.history = []cloudEntry{
		cloudEpisode("c1", "Bluey", 2018, 1, 1, "Magic Xylophone", "2026-11-21T10:00:00.000Z"),
		cloudEpisode("c2", "Bluey", 2018, 2, 5, "Hospital", "2026-11-22T08:00:00.000Z"),
		cloudSeason("c3", "Bluey", 2018, 2, "2026-11-20T10:00:00.000Z"),
		cloudShowItem("c4", "Bluey", 2018, "2026-11-19T10:00:00.000Z"),
		cloudEpisode("c5", "Bluey", 1992, 1, 1, "Pilot", "2026-11-18T10:00:00.000Z"),
		cloudEpisode("c6", "Peppa Pig", 0, 1, 1, "Muddy Puddles", "2026-11-17T10:00:00.000Z"),
		{ID: "c7", Date: "2026-11-16T10:00:00.000Z", Type: "MOVIE", Title: "Bluey"},
	}
	return r
}

// output is what the run printed, with the prompt.
func (r *clearHistoryRun) output() string {
	return r.out.String() + strings.Join(r.prompter.asked, "")
}

func assertContains(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The owner's plex.tv items of a target show go with the server's: episodes,
// seasons and the show, but not another year's show or a movie.
func TestClearHistory_PlexTV(t *testing.T) {
	r := newCloudRun(t, "y")
	before := slices.Clone(r.tv.history)
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	if want := keys("1", "2"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("server deleted %v; want %v", r.pms.deleted, want)
	}
	if want := []string{"c1", "c2", "c3", "c4"}; !slices.Equal(r.tv.removed, want) {
		t.Errorf("plex.tv removed %v; want %v", r.tv.removed, want)
	}
	if r.tv.userHits != 1 {
		t.Errorf("looked the owner up %d times; want once", r.tv.userHits)
	}
	assertContains(t, r.output(),
		"Watch history for QuienNZ on plex.tv:",
		`Bluey (2018)                             2 episode(s), 2 season or show item(s), last watched S02E05 "Hospital" on 2026-11-22`,
		"Not deleting these, which have another year: Bluey (1992)",
		"Delete these 2 server and 4 plex.tv history entries for QuienNZ (account 1)?",
		"Deleted 2 of 2 server and 4 of 4 plex.tv history entries.",
	)
	recs := r.records(t)
	if len(recs) != 1 || len(recs[0].PlexTV) != 4 || recs[0].PlexTV[0] != before[0] || !recs[0].Complete || recs[0].PlexTVSkipped != "" || recs[0].FailedPlexTV != nil {
		t.Errorf("records %+v; want one, complete, with the 4 plex.tv items", recs)
	}
}

// The record holds the plex.tv items before the first is removed, so an
// interrupted run leaves a trace of them.
func TestClearHistory_PlexTV_RecordedBeforeRemoving(t *testing.T) {
	r := newCloudRun(t, "y")
	checked := false
	r.tv.onRemove = func() {
		if checked {
			return
		}
		checked = true
		recs := r.records(t)
		if len(recs) != 1 || recs[0].Complete || len(recs[0].PlexTV) != 4 {
			t.Errorf("at the first removal, records %+v; want one, not complete, with the 4 plex.tv items", recs)
		}
	}
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Error("nothing was removed")
	}
}

// Answering no leaves plex.tv alone too.
func TestClearHistory_PlexTV_Declined(t *testing.T) {
	r := newCloudRun(t, "n")
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	r.assertUntouched(t)
}

// --account naming the owner clears plex.tv too.
func TestClearHistory_PlexTV_OwnerByName(t *testing.T) {
	r := newCloudRun(t, "y")
	if err := r.run(t, "QuienNZ", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if len(r.tv.removed) != 4 {
		t.Errorf("plex.tv removed %v; want the 4 Bluey (2018) items", r.tv.removed)
	}
}

// plex.tv is left alone, with a note saying why, when the account isn't the
// token owner's by exact name, or when plex.tv refuses the token for an
// --account run; the server is still cleared.
func TestClearHistory_PlexTV_Skipped(t *testing.T) {
	for _, tc := range []struct {
		name, account string
		set           func(*clearHistoryRun)
		wantDeleted   []string
		notice        string
	}{
		{"another account", "Stephanie", func(*clearHistoryRun) {}, keys("9"),
			"This PLEX_TOKEN can clear only its owner's (QuienNZ) plex.tv history, so any Stephanie has there is left alone."},
		{"owner not on the server", "QuienNZ", func(r *clearHistoryRun) { r.tv.username = "SomeoneElse" }, keys("1", "2"),
			"can clear only its owner's (SomeoneElse) plex.tv history"},
		// The account found by the name but for case may be someone else's.
		{"owner's name but for case", "", func(r *clearHistoryRun) { r.tv.username = "quiennz" }, keys("1", "2"),
			"The server account QuienNZ isn't named exactly as the PLEX_TOKEN owner quiennz"},
		{"401", "QuienNZ", func(r *clearHistoryRun) { r.tv.userStatus = http.StatusUnauthorized }, keys("1", "2"),
			"plex.tv refused this PLEX_TOKEN (401 Unauthorized)"},
		{"403", "QuienNZ", func(r *clearHistoryRun) { r.tv.userStatus = http.StatusForbidden }, keys("1", "2"),
			"plex.tv refused this PLEX_TOKEN (403 Forbidden)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newCloudRun(t, "y")
			tc.set(r)
			if err := r.run(t, tc.account, "Bluey (2018)"); err != nil {
				t.Fatal(err)
			}
			if r.tv.graphHits != 0 || !slices.Equal(r.pms.deleted, tc.wantDeleted) {
				t.Errorf("plex.tv requests %d, server deleted %v; want none and %v", r.tv.graphHits, r.pms.deleted, tc.wantDeleted)
			}
			assertContains(t, r.out.String(), tc.notice)
			recs := r.records(t)
			if len(recs) != 1 || recs[0].PlexTV != nil || !strings.Contains(recs[0].PlexTVSkipped, tc.notice) {
				t.Errorf("records %+v; want one with no plex.tv items, saying why", recs)
			}
		})
	}
}

// Any other failure to find the owner stops the run: clearing the server
// alone would leave the owner's plex.tv history behind.
func TestClearHistory_PlexTV_OwnerLookupErrorStops(t *testing.T) {
	for name, set := range map[string]func(*clearHistoryRun){
		"500":     func(r *clearHistoryRun) { r.tv.userStatus = http.StatusInternalServerError },
		"no uuid": func(r *clearHistoryRun) { r.tv.uuid = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newCloudRun(t, "y")
			set(r)
			err := r.run(t, "QuienNZ", "Bluey (2018)")
			if err == nil || !strings.HasSuffix(err.Error(), "nothing was deleted") {
				t.Errorf("err = %v; want an error saying nothing was deleted", err)
			}
			r.assertUntouched(t)
		})
	}
}

// A plex.tv history that can't be read stops the run before anything is deleted.
func TestClearHistory_PlexTV_HistoryErrorDeletesNothing(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.historyErr = "internal error"
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "internal error") || !strings.HasSuffix(err.Error(), "nothing was deleted") {
		t.Errorf("err = %v; want plex.tv's error and that nothing was deleted", err)
	}
	r.assertUntouched(t)
}

// A rate-limited history read is waited out like a delete.
func TestClearHistory_PlexTV_HistoryRateLimitWaited(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.historyRateLimits = 1
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if len(r.tv.removed) != 4 || !slices.Equal(r.slept, []time.Duration{time.Minute}) {
		t.Errorf("plex.tv removed %v after waiting %v; want 4 after a minute", r.tv.removed, r.slept)
	}
}

// A year-less target takes every year; a show plex.tv records no year for
// matches on title alone, and says so.
func TestClearHistory_PlexTV_Years(t *testing.T) {
	r := newCloudRun(t, "n")
	if err := r.run(t, "", "Bluey", "Peppa Pig (2004)"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	assertContains(t, out,
		"Bluey (1992)                             1 episode(s)",
		"Bluey (2018)                             2 episode(s)",
		"Peppa Pig                                1 episode(s)",
		"plex.tv records no year for a show these matched, so it matched on title alone: Peppa Pig (2004)",
	)
	if strings.Contains(out, "Not deleting these") {
		t.Errorf("a year-less target left a year out:\n%s", out)
	}
}

// A show left for another year isn't listed as kept when another target
// takes it.
func TestClearHistory_PlexTV_OtherYearTakenByAnother(t *testing.T) {
	r := newCloudRun(t, "n")
	if err := r.run(t, "", "Bluey (2018)", "Bluey (1992)"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	assertContains(t, out, "Bluey (1992)                             1 episode(s)")
	if strings.Contains(out, "Not deleting these") {
		t.Errorf("listed a taken show as kept:\n%s", out)
	}
}

// A show only plex.tv has is still cleared, and one it lacks is reported.
func TestClearHistory_PlexTV_MissingOnOneSide(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.history = append(r.tv.history, cloudEpisode("c8", "Bandit", 2020, 1, 1, "Ep", "2026-11-15T10:00:00.000Z"))
	if err := r.run(t, "", "Bandit", "NCIS: Los Angeles"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.tv.removed, []string{"c8"}) || !slices.Equal(r.pms.deleted, keys("5")) {
		t.Errorf("plex.tv removed %v, server deleted %v; want c8 and 5", r.tv.removed, r.pms.deleted)
	}
	assertContains(t, r.out.String(), "Not in plex.tv history:\n  NCIS: Los Angeles", "Not in this history:\n  Bandit")
}

// A show only plex.tv has is asked about by its count alone.
func TestClearHistory_PlexTV_OnlyPlexTVMatches(t *testing.T) {
	r := newCloudRun(t, "n")
	r.tv.history = append(r.tv.history, cloudEpisode("c8", "Bandit", 2020, 1, 1, "Ep", "2026-11-15T10:00:00.000Z"))
	if err := r.run(t, "", "Bandit"); err != nil {
		t.Fatal(err)
	}
	assertContains(t, r.output(), "Delete these 0 server and 1 plex.tv history entries for QuienNZ (account 1)?")
}

// A rate limit is waited out, then the delete goes through.
func TestClearHistory_PlexTV_RateLimitWaited(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.rateLimits, r.tv.retryAfter = 2, 61
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if len(r.tv.removed) != 4 {
		t.Errorf("plex.tv removed %v; want all 4", r.tv.removed)
	}
	if want := []time.Duration{61 * time.Second, 61 * time.Second}; !slices.Equal(r.slept, want) {
		t.Errorf("slept %v; want %v", r.slept, want)
	}
	assertContains(t, r.out.String(), "plex.tv rate limit: waiting 1m1s")
}

// A wait plex.tv doesn't give is a minute, and one past the cap is capped.
func TestClearHistory_PlexTV_RateLimitWaitBounds(t *testing.T) {
	for _, tc := range []struct {
		after float64
		want  time.Duration
	}{{0, time.Minute}, {3600, maxRateLimitWait}} {
		r := newCloudRun(t, "y")
		r.tv.rateLimits, r.tv.retryAfter = 1, tc.after
		if err := r.run(t, "", "Bluey (2018)"); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.slept, []time.Duration{tc.want}) {
			t.Errorf("retryAfter %v: slept %v; want %v", tc.after, r.slept, tc.want)
		}
	}
}

// A delete still rate-limited after maxRateLimitTries tries fails, so each
// waits maxRateLimitTries-1 times; three such failures in a row stop the
// plex.tv deletes, and the record lists every item not deleted.
func TestClearHistory_PlexTV_RateLimitGivesUp(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.rateLimits = 1000
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), `4 history entries were not deleted`) || !strings.HasSuffix(err.Error(), `under "failedPlexTV"`) {
		t.Fatalf("err = %v; want 4 not deleted, under failedPlexTV", err)
	}
	if want := 3 * (maxRateLimitTries - 1); len(r.slept) != want {
		t.Errorf("slept %d times; want %d (%d tries for each of 3 items)", len(r.slept), want, maxRateLimitTries)
	}
	if want := keys("1", "2"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("server deleted %v; want %v, before plex.tv", r.pms.deleted, want)
	}
	recs := r.records(t)
	if len(recs) != 1 || recs[0].Failed != nil || !slices.Equal(recs[0].FailedPlexTV, []string{"c1", "c2", "c3", "c4"}) || !recs[0].Complete {
		t.Errorf("records %+v; want one listing c1–c4 as failed on plex.tv", recs)
	}
	assertContains(t, r.out.String(), "Stopped after 3 failures in a row on plex.tv; 1 entries there weren't tried.",
		fmt.Sprintf("gave up after %d tries", maxRateLimitTries), "Deleted 2 of 2 server and 0 of 4 plex.tv history entries.")
}

// A plex.tv error on one item is reported, not retried, and the rest go ahead.
func TestClearHistory_PlexTV_OneFailure(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failRemove = map[string]bool{"c3": true}
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "1 history entries were not deleted") {
		t.Fatalf("err = %v; want one not deleted", err)
	}
	if want := []string{"c1", "c2", "c4"}; !slices.Equal(r.tv.removed, want) {
		t.Errorf("plex.tv removed %v; want %v", r.tv.removed, want)
	}
	if len(r.slept) != 0 {
		t.Errorf("waited %v for an error that isn't a rate limit", r.slept)
	}
	assertContains(t, r.out.String(), "failed: plex.tv Bluey (2018) season 2 on 2026-11-20: plex.tv: removeActivity: boom")
	if strings.Contains(r.out.String(), "said it removed") {
		t.Errorf("the failed item is reported as one plex.tv said it removed:\n%s", r.out.String())
	}
}

// Failures on both sides are listed apart in the record.
func TestClearHistory_PlexTV_FailuresOnBothSides(t *testing.T) {
	r := newCloudRun(t, "y")
	r.pms.failKeys = map[string]bool{historyKeyPrefix + "1": true}
	r.tv.failRemove = map[string]bool{"c3": true}
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "2 history entries were not deleted") || !strings.HasSuffix(err.Error(), `under "failed" and "failedPlexTV"`) {
		t.Fatalf("err = %v; want 2 not deleted, under both fields", err)
	}
	recs := r.records(t)
	if len(recs) != 1 || !slices.Equal(recs[0].Failed, keys("1")) || !slices.Equal(recs[0].FailedPlexTV, []string{"c3"}) {
		t.Errorf("records %+v; want server key 1 and plex.tv c3 failed", recs)
	}
}

func planCloudFor(t *testing.T, entries []cloudEntry, targets ...string) cloudPlan {
	t.Helper()
	ts, err := parseHistoryTargets(targets)
	if err != nil {
		t.Fatal(err)
	}
	return planCloud(entries, ts)
}

func matchedIDs(plan cloudPlan) []string {
	var ids []string
	for _, e := range plan.matched {
		ids = append(ids, e.ID)
	}
	return ids
}

// Items are grouped by plex.tv's show id: an item that gives no year is its
// show's, and a year-less show beside one with the target's title and year
// is another show, not taken.
func TestPlanCloud_ByShowID(t *testing.T) {
	yearless := cloudEpisode("a2", "Bluey", 0, 1, 2, "Ep", "")
	yearless.ShowID = showID("Bluey", 2018)                 // Bluey (2018), with no year on this item
	other := cloudEpisode("b1", "Bluey", 0, 1, 1, "Ep", "") // another show, with no year
	plan := planCloudFor(t, []cloudEntry{cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", ""), yearless, other}, "Bluey (2018)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "a2"}) {
		t.Errorf("matched %v; want a1 and a2", got)
	}
	if len(plan.shows) != 1 || len(plan.yearUnchecked) != 0 || !slices.Equal(plan.yearless, []string{"Bluey"}) {
		t.Errorf("shows %v, unchecked %v, kept year-less %v; want one show, checked, and Bluey kept", plan.shows, plan.yearUnchecked, plan.yearless)
	}
}

// Two shows with the same title and year are told apart by id, and flagged.
func TestPlanCloud_SameTitleAndYear(t *testing.T) {
	a := cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", "")
	b := cloudEpisode("b1", "Bluey", 2018, 1, 1, "Ep", "")
	b.ShowID = "sh-other"
	plan := planCloudFor(t, []cloudEntry{a, b}, "Bluey (2018)")
	if len(plan.shows) != 2 {
		t.Fatalf("shows %v; want 2", plan.shows)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "Bluey (2018) [sh-Bluey-2018]", "Bluey (2018) [sh-other]", "may be different shows")
}

// A year plex.tv gives in the title alone is the show's year, shown once.
func TestPlanCloud_YearInTitle(t *testing.T) {
	e := cloudEpisode("a1", "Doctor Who (2005)", 0, 1, 1, "Rose", "")
	plan := planCloudFor(t, []cloudEntry{e}, "Doctor Who (2005)")
	if !slices.Equal(matchedIDs(plan), []string{"a1"}) || len(plan.yearUnchecked) != 0 {
		t.Fatalf("matched %v, unchecked %v; want a1, checked", matchedIDs(plan), plan.yearUnchecked)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	if s := out.String(); !strings.Contains(s, "Doctor Who (2005) ") || strings.Contains(s, "(2005) (2005)") {
		t.Errorf("output:\n%s", s)
	}
}

// The item watched last is found by time, not by text: ".500Z" sorts before
// "Z" but is later.
func TestPlanCloud_LastWatchedByTime(t *testing.T) {
	plan := planCloudFor(t, []cloudEntry{
		cloudEpisode("a1", "Bluey", 2018, 1, 1, "Later", "2026-11-22T08:00:00.500Z"),
		cloudEpisode("a2", "Bluey", 2018, 1, 2, "Earlier", "2026-11-22T08:00:00Z"),
	}, "Bluey")
	for _, sum := range plan.shows {
		if sum.last.ID != "a1" {
			t.Errorf("last watched %s; want a1", sum.last.ID)
		}
	}
}

// plex.tv's reply to a removal isn't trusted: the history is read again, a
// second time after a wait when it still lists them, and each item still
// listed is reported, though plex.tv said it was removed.
func TestClearHistory_PlexTV_ClaimedButStillListed(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.ignoreRemove = true
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.HasSuffix(err.Error(), `under "failedPlexTV"`) {
		t.Fatalf("err = %v; want the items listed under failedPlexTV", err)
	}
	assertContains(t, r.out.String(), "plex.tv still lists 4 item(s) it was asked to remove; checking again in 10s.",
		"plex.tv still lists 4 of the items it said it removed:\n",
		`  still listed: plex.tv Bluey (2018) S01E01 "Magic Xylophone" on 2026-11-21`,
		"  still listed: plex.tv Bluey (2018) the show on 2026-11-19",
		"Deleted 2 of 2 server and 0 of 4 plex.tv history entries.")
	if strings.Contains(r.out.String(), "no longer lists") {
		t.Errorf("output reports items gone:\n%s", r.out.String())
	}
	if !slices.Equal(r.slept, []time.Duration{settleWait}) {
		t.Errorf("waited %v; want one settle wait", r.slept)
	}
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c1", "c2", "c3", "c4"}) {
		t.Errorf("records %+v; want c1–c4 failed", recs)
	}
}

// Removals plex.tv shows only after a while count once the second read
// doesn't list them.
func TestClearHistory_PlexTV_SettlesAfterWait(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.ignoreRemove = true
	r.onSleep = func() {
		r.tv.history = slices.DeleteFunc(r.tv.history, func(e cloudEntry) bool { return slices.Contains(r.tv.removed, e.ID) })
	}
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.slept, []time.Duration{settleWait}) {
		t.Errorf("waited %v; want one settle wait", r.slept)
	}
	assertContains(t, r.out.String(), "Deleted 2 of 2 server and 4 of 4 plex.tv history entries.")
	if recs := r.records(t); len(recs) != 1 || recs[0].FailedPlexTV != nil || recs[0].PlexTVUnchecked != "" {
		t.Errorf("records %+v; want nothing failed", recs)
	}
}

// A removal plex.tv answers without saying it took is checked in the
// history, and doesn't count toward the stop after failures in a row: each
// item is tried.
func TestClearHistory_PlexTV_RemovedDespiteReply(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.removeResult = nil // plex.tv removes the item but replies null
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"c1", "c2", "c3", "c4"}; !slices.Equal(r.tv.removed, want) {
		t.Errorf("plex.tv was asked to remove %v; want %v", r.tv.removed, want)
	}
	assertContains(t, r.out.String(), "  unconfirmed: plex.tv Bluey (2018) season 2 on 2026-11-20:",
		"plex.tv no longer lists 4 of the items whose removal it didn't confirm.",
		"Deleted 2 of 2 server and 4 of 4 plex.tv history entries.")
	if out := r.out.String(); strings.Contains(out, "said it removed") || strings.Contains(out, "Stopped after") {
		t.Errorf("output:\n%s", out)
	}
	if recs := r.records(t); len(recs) != 1 || recs[0].FailedPlexTV != nil {
		t.Errorf("records %+v; want nothing failed", recs)
	}
}

// Unconfirmed removals that didn't take stay failed.
func TestClearHistory_PlexTV_UnconfirmedAndListed(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.removeResult, r.tv.ignoreRemove = false, true
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "4 history entries were not deleted") {
		t.Fatalf("err = %v; want all 4 not deleted", err)
	}
	if len(r.tv.removed) != 4 || !slices.Equal(r.slept, []time.Duration{settleWait}) {
		t.Errorf("asked to remove %v, waited %v; want all 4 tried and one settle wait", r.tv.removed, r.slept)
	}
	if strings.Contains(r.out.String(), "said it removed") {
		t.Errorf("an unconfirmed removal is reported as one plex.tv said it made:\n%s", r.out.String())
	}
	assertContains(t, r.out.String(), "plex.tv still lists 4 of the items whose removal it didn't confirm:\n",
		"  still listed: plex.tv Bluey (2018) the show on 2026-11-19")
}

// An item whose removal failed with an error stays failed though plex.tv no
// longer lists it: only a reply that doesn't say is overruled.
func TestClearHistory_PlexTV_ErrorNotOverruled(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failRemove = map[string]bool{"c3": true}
	r.tv.onRemove = func() {
		if len(r.tv.removed) == 2 { // c3 goes, and its removal fails anyway
			r.tv.history = slices.DeleteFunc(r.tv.history, func(e cloudEntry) bool { return e.ID == "c3" })
		}
	}
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "1 history entries were not deleted") {
		t.Fatalf("err = %v; want c3 not deleted", err)
	}
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c3"}) || recs[0].PlexTVUnchecked != "" {
		t.Errorf("records %+v; want c3 failed, checked", recs)
	}
	if len(r.slept) != 0 {
		t.Errorf("waited %v for an item whose removal failed with an error", r.slept)
	}
}

// Some removed, one claimed but still listed and one failed: each is
// reported for what it is.
func TestClearHistory_PlexTV_MixedOutcome(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failRemove, r.tv.keepListed = map[string]bool{"c3": true}, map[string]bool{"c2": true}
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "2 history entries were not deleted") {
		t.Fatalf("err = %v; want c2 and c3 not deleted", err)
	}
	assertContains(t, r.out.String(), "plex.tv still lists 1 item(s) it was asked to remove; checking again in 10s.",
		"plex.tv still lists 1 of the items it said it removed:\n"+`  still listed: plex.tv Bluey (2018) S02E05 "Hospital"`,
		"Deleted 2 of 2 server and 2 of 4 plex.tv history entries.")
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c2", "c3"}) {
		t.Errorf("records %+v; want c2 and c3 failed", recs)
	}
}

// When plex.tv can't be read again after the deletes, the replies stand, the
// record says why, and the run doesn't claim success.
func TestClearHistory_PlexTV_RecheckFails(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failReadAfter = true
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "plex.tv's deletes couldn't be checked") {
		t.Fatalf("err = %v; want the unchecked deletes reported", err)
	}
	assertContains(t, r.out.String(), "Deleted 2 of 2 server and 4 of 4 plex.tv history entries (plex.tv's by its replies, not checked).")
	recs := r.records(t)
	if len(recs) != 1 || recs[0].FailedPlexTV != nil || !strings.Contains(recs[0].PlexTVUnchecked, "read after removal failed") || !recs[0].Complete {
		t.Errorf("records %+v; want complete, nothing failed, saying why it's unchecked", recs)
	}
}

// A failed removal stays failed when the re-read fails, and the error names
// both problems.
func TestClearHistory_PlexTV_RecheckFailsAfterFailure(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failRemove, r.tv.failReadAfter = map[string]bool{"c3": true}, true
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "1 history entries were not deleted") || !strings.Contains(err.Error(), "couldn't be checked") {
		t.Fatalf("err = %v; want both problems", err)
	}
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c3"}) || recs[0].PlexTVUnchecked == "" {
		t.Errorf("records %+v; want c3 failed and unchecked", recs)
	}
}

// A re-read that comes back empty or short can't pass for removals that
// took: items it should still list aren't there, so the deletes are
// unchecked, and failed or untried ones stay failed.
func TestClearHistory_PlexTV_ShortRereadUntrusted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fail       map[string]bool
		lose       func(cloudEntry) bool
		wantFailed []string
		wantLost   string
	}{
		{"empty, all failed", map[string]bool{"c1": true, "c2": true, "c3": true, "c4": true},
			func(cloudEntry) bool { return true }, []string{"c1", "c2", "c3", "c4"}, "lacks 4 item(s)"},
		{"an untried item gone", map[string]bool{"c1": true, "c2": true, "c3": true},
			func(e cloudEntry) bool { return e.ID == "c4" }, []string{"c1", "c2", "c3", "c4"}, "lacks 1 item(s)"},
		{"an unmatched item gone", nil,
			func(e cloudEntry) bool { return e.ID == "c5" }, nil, "lacks 1 item(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newCloudRun(t, "y")
			r.tv.failRemove = tc.fail
			r.tv.onRemove = func() { r.tv.history = slices.DeleteFunc(r.tv.history, tc.lose) }
			err := r.run(t, "", "Bluey (2018)")
			if err == nil || !strings.Contains(err.Error(), "couldn't be checked") {
				t.Fatalf("err = %v; want the deletes unchecked", err)
			}
			recs := r.records(t)
			if len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, tc.wantFailed) || !strings.Contains(recs[0].PlexTVUnchecked, tc.wantLost) {
				t.Errorf("records %+v; want failed %v and unchecked %q", recs, tc.wantFailed, tc.wantLost)
			}
			if strings.Contains(r.out.String(), "no longer lists") {
				t.Errorf("output takes the short read as removals:\n%s", r.out.String())
			}
		})
	}
}

// The re-read waits out a rate limit like any other plex.tv request.
func TestClearHistory_PlexTV_RereadRateLimitWaited(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.onRemove = func() { r.tv.historyRateLimits = 1 } // only reads after a removal see it
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.slept, []time.Duration{time.Minute}) {
		t.Errorf("waited %v; want a minute for the re-read", r.slept)
	}
}

// Matching items the plan didn't have (watched mid-run, or paged into view)
// are reported for another run, not deleted.
func TestClearHistory_PlexTV_NewMatchesReported(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.onRemove = func() {
		if !slices.ContainsFunc(r.tv.history, func(e cloudEntry) bool { return e.ID == "c9" }) {
			r.tv.history = append(r.tv.history, cloudEpisode("c9", "Bluey", 2018, 3, 1, "Ghostbasket", "2026-11-22T09:00:00.000Z"))
		}
	}
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "plex.tv now lists 1 more matching item(s); run again to clear them") {
		t.Fatalf("err = %v; want the new item reported", err)
	}
	if slices.Contains(r.tv.removed, "c9") {
		t.Error("removed the item the plan didn't have")
	}
	if recs := r.records(t); len(recs) != 1 || recs[0].FailedPlexTV != nil {
		t.Errorf("records %+v; want nothing failed", recs)
	}
}

// When the record can't be rewritten, the items plex.tv still lists are
// named in the output, and the error says so beside the rewrite failure.
func TestClearHistory_PlexTV_RecordUpdateFails(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	r := newCloudRun(t, "y")
	r.blockRecordOnDelete(t)
	r.tv.keepListed = map[string]bool{"c2": true}
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "couldn't be updated to say so") ||
		!strings.Contains(err.Error(), "1 history entries were not deleted; the lines above name them") {
		t.Fatalf("err = %v; want the rewrite failure and the item not deleted", err)
	}
	assertContains(t, r.out.String(), `  still listed: plex.tv Bluey (2018) S02E05 "Hospital"`)
}

// A history read still rate-limited after every try stops the run before
// anything is deleted.
func TestClearHistory_PlexTV_HistoryRateLimitGivesUp(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.historyRateLimits = 1000
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "gave up after") || !strings.HasSuffix(err.Error(), "nothing was deleted") {
		t.Errorf("err = %v; want a gave-up rate limit and nothing deleted", err)
	}
	if len(r.slept) != maxRateLimitTries-1 {
		t.Errorf("waited %d times; want %d", len(r.slept), maxRateLimitTries-1)
	}
	r.assertUntouched(t)
}

// The record's keys are the ones the README names, and failedFields names
// in the error message.
func TestHistoryRecord_Keys(t *testing.T) {
	rec := historyRecord{Failed: []string{"k"}, FailedPlexTV: []string{"c"}, PlexTVSkipped: "why", PlexTVUnchecked: "why"}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"plexTV", "plexTVSkipped", "plexTVUnchecked", "failed", "failedPlexTV"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("record JSON lacks %q: %s", k, data)
		}
	}
	for _, name := range strings.Split(failedFields(rec), " and ") {
		if _, ok := keys[strings.Trim(name, `"`)]; !ok {
			t.Errorf("failedFields names %s, which the record JSON lacks", name)
		}
	}
}

// An item with plex.tv's show id but no title goes with the show's titled
// items; items naming no show at all are counted and left alone.
func TestPlanCloud_ItemsWithoutTitle(t *testing.T) {
	untitled := cloudSeason("a2", "", 0, 1, "")
	untitled.ShowID = showID("Bluey", 2018)
	plan := planCloudFor(t, []cloudEntry{
		untitled, cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", ""), // the title comes from a later item
		{ID: "n1", Type: cloudTypeEpisode}, // no show at all
		{ID: "n2"},                         // plex.tv sent no item
		{ID: "n3", Type: "MOVIE", Title: "Bluey"},
		{ID: "n4", Type: cloudTypeSeason, ShowID: "untitled"}, // a show id no item titles
	}, "Bluey (2018)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a2", "a1"}) {
		t.Errorf("matched %v; want a2 and a1", got)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "plex.tv lists 3 item(s) that name no show to match, so they're left alone.")
}

// A show plex.tv gives no id or year beside one with the target's year is
// another show, so it's left, and the plan says so.
func TestPlanCloud_YearlessBesideTargetYear(t *testing.T) {
	lost := cloudEpisode("b1", "Bluey", 0, 1, 1, "Ep", "")
	lost.ShowID = ""
	plan := planCloudFor(t, []cloudEntry{cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", ""), lost}, "Bluey (2018)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1"}) || plan.yearUnchecked != nil {
		t.Errorf("matched %v, year unchecked %v; want only a1", got, plan.yearUnchecked)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "Not deleting these, which plex.tv gives no year while the title is there with the year asked for: Bluey")
}

// A year-less show beside only another year's matches on
// title alone: the other year's show can't be the target, the year-less one can.
func TestPlanCloud_YearlessBesideAnotherYear(t *testing.T) {
	lost := cloudEpisode("b1", "Bluey", 0, 1, 1, "Ep", "")
	lost.ShowID = ""
	plan := planCloudFor(t, []cloudEntry{cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", ""), lost}, "Bluey (1992)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"b1"}) || !slices.Equal(plan.yearUnchecked, []string{"Bluey (1992)"}) {
		t.Errorf("matched %v, year unchecked %v; want b1 on title alone", got, plan.yearUnchecked)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "Not deleting these, which have another year: Bluey (2018)",
		"plex.tv records no year for a show these matched, so it matched on title alone: Bluey (1992)")
	if strings.Contains(out.String(), "gives no year while") {
		t.Errorf("the year-less show is listed as kept:\n%s", out.String())
	}
}

// plex.tv titles match as server titles do: case, punctuation and full
// width folded.
func TestPlanCloud_TitleFolding(t *testing.T) {
	plan := planCloudFor(t, []cloudEntry{
		cloudEpisode("a1", "NCIS Los Angeles", 2009, 1, 1, "Ep", ""),
		cloudEpisode("b1", "ＢＬＥＡＣＨ", 2004, 1, 1, "Ep", ""),
	}, "ncis: los angeles", "Bleach (2004)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "b1"}) {
		t.Errorf("matched %v; want both", got)
	}
}

// Dates show in local time, as the server's plan does.
func TestDescribeCloudItem_LocalDate(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("NZDT", 13*60*60)
	t.Cleanup(func() { time.Local = saved })
	e := cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", "2026-11-17T20:00:00Z")
	if got := describeCloudItem(e); !strings.HasSuffix(got, " on 2026-11-18") {
		t.Errorf("describeCloudItem = %q; want the NZ date 2026-11-18", got)
	}
}

// A date that can't be read isn't printed, and doesn't hide a readable later one.
func TestPlanCloud_UnreadableDate(t *testing.T) {
	plan := planCloudFor(t, []cloudEntry{
		cloudEpisode("a1", "Bluey", 2018, 1, 1, "Odd", "1767225600"),
		cloudEpisode("a2", "Bluey", 2018, 1, 2, "Later", "2026-11-22T08:00:00Z"),
	}, "Bluey")
	for _, sum := range plan.shows {
		if sum.last.ID != "a2" {
			t.Errorf("last watched %s; want a2", sum.last.ID)
		}
	}
	if got := describeCloudItem(cloudEpisode("a1", "Bluey", 2018, 1, 1, "Odd", "1767225600")); strings.Contains(got, " on ") {
		t.Errorf("describeCloudItem = %q; want no date", got)
	}
}

// Failure lines name a show whose title carries its year once.
func TestCloudPlanDescribe_YearInTitle(t *testing.T) {
	e := cloudEpisode("a1", "Doctor Who (2005)", 2005, 1, 1, "Rose", "")
	if got := planCloudFor(t, []cloudEntry{e}, "Doctor Who").describe(e); got != `plex.tv Doctor Who (2005) S01E01 "Rose"` {
		t.Errorf("describe = %q", got)
	}
}

// An item without its show's title is named by the show it was grouped with.
func TestCloudPlanDescribe_UntitledItem(t *testing.T) {
	untitled := cloudSeason("a2", "", 0, 1, "")
	untitled.ShowID = showID("Bluey", 2018)
	plan := planCloudFor(t, []cloudEntry{cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", ""), untitled}, "Bluey")
	if got := plan.describe(untitled); got != "plex.tv Bluey (2018) season 1" {
		t.Errorf("describe = %q; want the grouped show's name", got)
	}
}

// A year-less show the plan kept beside the target's year stays kept when
// the run checks for new items, though the dated show's items are gone.
func TestClearHistory_PlexTV_KeptYearlessNotNew(t *testing.T) {
	r := newCloudRun(t, "y")
	other := cloudEpisode("b1", "Bluey", 0, 1, 1, "Other", "2026-11-15T10:00:00.000Z")
	other.ShowID = ""
	r.tv.history = append(r.tv.history, other)
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.out.String(), "didn't have") || slices.Contains(r.tv.removed, "b1") {
		t.Errorf("the kept show is reported new or removed:\n%s", r.out.String())
	}
}

// An episode of that kept show watched during the run isn't reported either:
// planned with the first read, its show is still beside the target's year.
func TestClearHistory_PlexTV_KeptYearlessNewEpisodeNotNew(t *testing.T) {
	r := newCloudRun(t, "y")
	other := cloudEpisode("b1", "Bluey", 0, 1, 1, "Other", "2026-11-15T10:00:00.000Z")
	other.ShowID = ""
	r.tv.history = append(r.tv.history, other)
	r.tv.onRemove = func() {
		if !slices.ContainsFunc(r.tv.history, func(e cloudEntry) bool { return e.ID == "b2" }) {
			ep := cloudEpisode("b2", "Bluey", 0, 1, 2, "Another", "2026-11-22T11:00:00.000Z")
			ep.ShowID = ""
			r.tv.history = append([]cloudEntry{ep}, r.tv.history...)
		}
	}
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.out.String(), "didn't have") {
		t.Errorf("the kept show's new episode is reported:\n%s", r.out.String())
	}
}

// With nothing older than an item still listed, the re-read can't show it
// went: an unconfirmed removal of it stays failed, a confirmed one stands.
func TestClearHistory_PlexTV_UnreachedItems(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reply      any
		wantFailed []string
	}{
		{"unconfirmed", nil, []string{"c1", "c2", "c3", "c4"}},
		{"confirmed", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newCloudRun(t, "y")
			r.tv.history = r.tv.history[:4] // only the target show
			r.tv.removeResult, r.tv.ignoreRemove = tc.reply, true
			r.tv.onRemove = func() { r.tv.history = nil } // the re-read comes back empty
			err := r.run(t, "", "Bluey (2018)")
			recs := r.records(t)
			if len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, tc.wantFailed) || recs[0].PlexTVUnchecked != "" {
				t.Fatalf("records %+v; want failed %v, checked", recs, tc.wantFailed)
			}
			if tc.wantFailed == nil {
				if err != nil {
					t.Errorf("err = %v; want none", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "4 history entries were not deleted") {
				t.Errorf("err = %v; want all 4 not deleted", err)
			}
			assertContains(t, r.out.String(), "plex.tv lists nothing older than 4 of the items whose removal it didn't confirm")
			if strings.Contains(r.out.String(), "no longer lists") {
				t.Errorf("output takes the empty read as removals:\n%s", r.out.String())
			}
		})
	}
}

// When the read after the wait fails or comes back short, the first re-read,
// which showed the items still there, stands.
func TestClearHistory_PlexTV_SettleReadFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(*clearHistoryRun)
	}{
		{"error", func(r *clearHistoryRun) { r.tv.failReadAfter = true }},
		{"short", func(r *clearHistoryRun) { r.tv.history = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newCloudRun(t, "y")
			r.tv.ignoreRemove = true
			r.onSleep = func() { tc.after(r) }
			err := r.run(t, "", "Bluey (2018)")
			if err == nil || !strings.Contains(err.Error(), "4 history entries were not deleted") || strings.Contains(err.Error(), "couldn't be checked") {
				t.Fatalf("err = %v; want 4 not deleted, checked", err)
			}
			assertContains(t, r.out.String(), "so the first read stands.", "Deleted 2 of 2 server and 0 of 4 plex.tv history entries.")
			if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c1", "c2", "c3", "c4"}) {
				t.Errorf("records %+v; want c1–c4 failed", recs)
			}
		})
	}
}

// When the record can't be rewritten, entries not tried are named too.
func TestClearHistory_PlexTV_RecordUpdateFailsNamesUntried(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	r := newCloudRun(t, "y")
	r.blockRecordOnDelete(t)
	r.tv.failRemove = map[string]bool{"c1": true, "c2": true, "c3": true}
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "4 history entries were not deleted; the lines above name them") {
		t.Fatalf("err = %v; want 4 not deleted, named above", err)
	}
	assertContains(t, r.out.String(), "Not deleted:\n", "\n  plex.tv Bluey (2018) the show on 2026-11-19\n")
}

// Unconfirmed replies are plex.tv answering, so they break a run of failures.
func TestDeleteEach_UnconfirmedBreaksRun(t *testing.T) {
	boom, odd := errors.New("boom"), &unconfirmedError{"odd"}
	errs := []error{boom, boom, odd, boom, boom, nil}
	var tried []string
	ids := []string{"a", "b", "c", "d", "e", "f"}
	res := deleteEach(io.Discard, "plex.tv", ids, func(s string) string { return s }, func(s string) string { return s },
		func(id string) error { tried = append(tried, id); return errs[len(tried)-1] })
	if !slices.Equal(tried, ids) || len(res.untried) != 0 || !res.unconfirmed["c"] {
		t.Errorf("tried %v, untried %v, unconfirmed %v; want all tried and c unconfirmed", tried, res.untried, res.unconfirmed)
	}
}

// A target with a year and one without, of one title: the year-less one
// takes the year-less show, which isn't then listed as kept.
func TestPlanCloud_DatedAndUndatedTargets(t *testing.T) {
	other := cloudEpisode("b1", "Bluey", 0, 1, 1, "Ep", "")
	other.ShowID = ""
	plan := planCloudFor(t, []cloudEntry{cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", ""), other}, "Bluey (2018)", "Bluey")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "b1"}) || plan.yearless != nil || plan.yearUnchecked != nil {
		t.Errorf("matched %v, kept %v, unchecked %v; want both matched, nothing kept or unchecked", got, plan.yearless, plan.yearUnchecked)
	}
}
