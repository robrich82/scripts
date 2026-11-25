package main

import (
	"encoding/json"
	"fmt"
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

// plex.tv's reply to a removal isn't trusted: the history is read again, and
// an item it still lists is reported, though plex.tv said it was removed.
func TestClearHistory_PlexTV_ClaimedButStillListed(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.ignoreRemove = true
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.HasSuffix(err.Error(), `under "failedPlexTV"`) {
		t.Fatalf("err = %v; want the items listed under failedPlexTV", err)
	}
	assertContains(t, r.out.String(), "plex.tv still lists 4 of the items it said it removed.", "Deleted 2 of 2 server and 0 of 4 plex.tv history entries.")
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c1", "c2", "c3", "c4"}) {
		t.Errorf("records %+v; want c1–c4 failed", recs)
	}
}

// A removal whose reply reads as a failure but that took is counted as done,
// and an item never tried after a run of such failures is still listed.
func TestClearHistory_PlexTV_RemovedDespiteReply(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.removeResult = nil // plex.tv removes the item but replies null
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "1 history entries were not deleted") {
		t.Fatalf("err = %v; want only the untried item not deleted", err)
	}
	assertContains(t, r.out.String(), "plex.tv no longer lists 3 of the items whose removal looked like it failed.")
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c4"}) {
		t.Errorf("records %+v; want only c4 failed", recs)
	}
}

// When plex.tv can't be read again after the deletes, the replies stand, the
// record says why, and the run doesn't claim success.
func TestClearHistory_PlexTV_RecheckFails(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failReadAfter = true
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "couldn't be re-read to check its deletes") {
		t.Fatalf("err = %v; want the unchecked deletes reported", err)
	}
	recs := r.records(t)
	if len(recs) != 1 || recs[0].FailedPlexTV != nil || !strings.Contains(recs[0].PlexTVUnchecked, "read after removal failed") || !recs[0].Complete {
		t.Errorf("records %+v; want complete, nothing failed, saying why it's unchecked", recs)
	}
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

// The record's keys are the ones the error message and README name.
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
	}, "Bluey (2018)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a2", "a1"}) {
		t.Errorf("matched %v; want a2 and a1", got)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "plex.tv lists 2 item(s) that name no show to match, so they're left alone.")
}

// A show plex.tv gives no id or year may be a dated show of the same title
// with its id and year missing, so a dated target leaves it, and says so.
func TestPlanCloud_YearlessBesideAnotherYear(t *testing.T) {
	lost := cloudEpisode("b1", "Bluey", 0, 1, 1, "Ep", "")
	lost.ShowID = ""
	plan := planCloudFor(t, []cloudEntry{cloudEpisode("a1", "Bluey", 2018, 1, 1, "Ep", ""), lost}, "Bluey (1992)")
	if len(plan.matched) != 0 || !slices.Equal(plan.missing, []string{"Bluey (1992)"}) {
		t.Errorf("matched %v, missing %v; want nothing taken", matchedIDs(plan), plan.missing)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "Not deleting these, which have another year: Bluey (2018)",
		"Not deleting these, which plex.tv gives no year while the title is there with one: Bluey")
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
func TestDescribeCloudEntry_YearInTitle(t *testing.T) {
	e := cloudEpisode("a1", "Doctor Who (2005)", 2005, 1, 1, "Rose", "")
	if got := describeCloudEntry(e); got != `plex.tv Doctor Who (2005) S01E01 "Rose"` {
		t.Errorf("describeCloudEntry = %q", got)
	}
}
