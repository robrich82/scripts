package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// newCloudRun is newClearHistoryRun with the owner's plex.tv history: four
// plays of Heat (1995), one of Heat (1986), The Thing with no year, and an
// episode titled Heat.
func newCloudRun(t *testing.T, answers ...string) *clearHistoryRun {
	r := newClearHistoryRun(t, answers...)
	r.tv.history = []cloudEntry{
		cloudPlay("c1", "Heat", 1995, "2026-11-21T10:00:00.000Z"),
		cloudPlay("c2", "Heat", 1995, "2026-11-22T08:00:00.000Z"),
		cloudPlay("c3", "Heat", 1995, "2026-11-20T10:00:00.000Z"),
		cloudPlay("c4", "Heat", 1995, "2026-11-19T10:00:00.000Z"),
		cloudPlay("c5", "Heat", 1986, "2026-11-18T10:00:00.000Z"),
		cloudPlay("c6", "The Thing", 0, "2026-11-17T10:00:00.000Z"),
		{ID: "c7", Date: "2026-11-16T10:00:00.000Z", Type: "EPISODE", Title: "Heat"},
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

// watched is how the plan names a plex.tv date: its local day.
func watched(date string) string {
	return ", last watched " + cloudTime(cloudEntry{Date: date}).Local().Format(time.DateOnly)
}

// planLine is a plex.tv plan line for a movie.
func planLine(name string, plays int, date string) string {
	return fmt.Sprintf("  %-40s %d play(s)%s", name, plays, watched(date))
}

// The owner's plex.tv plays of a target movie go with the server's, but not
// another year's movie or a show's episode with the title.
func TestClearHistory_PlexTV(t *testing.T) {
	r := newCloudRun(t, "y")
	before := slices.Clone(r.tv.history)
	if err := r.run(t, "", "Heat (1995)"); err != nil {
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
		"Watch history for QuienNZ on plex.tv:\n"+planLine("Heat (1995)", 4, "2026-11-22T08:00:00.000Z")+"\n",
		"Not deleting these, which have another year: Heat (1986)",
		"Delete these 2 server and 4 plex.tv history entries for QuienNZ (account 1)?",
		"Deleted 2 of 2 server and 4 of 4 plex.tv history entries.",
	)
	recs := r.records(t)
	if len(recs) != 1 || len(recs[0].PlexTV) != 4 || recs[0].PlexTV[0] != before[0] || !recs[0].Complete || recs[0].PlexTVSkipped != "" || recs[0].FailedPlexTV != nil {
		t.Errorf("records %+v; want one, complete, with the 4 plex.tv plays", recs)
	}
}

// The record holds the plex.tv plays before the first is removed, so an
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
			t.Errorf("at the first removal, records %+v; want one, not complete, with the 4 plex.tv plays", recs)
		}
	}
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Error("nothing was removed")
	}
}

// Answering no leaves plex.tv alone too.
func TestClearHistory_PlexTV_Declined(t *testing.T) {
	r := newCloudRun(t, "n")
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	r.assertUntouched(t)
}

// --account naming the owner clears plex.tv too.
func TestClearHistory_PlexTV_OwnerByName(t *testing.T) {
	r := newCloudRun(t, "y")
	if err := r.run(t, "QuienNZ", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	if len(r.tv.removed) != 4 {
		t.Errorf("plex.tv removed %v; want the 4 Heat (1995) plays", r.tv.removed)
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
			if err := r.run(t, tc.account, "Heat (1995)"); err != nil {
				t.Fatal(err)
			}
			if r.tv.graphHits != 0 || !slices.Equal(r.pms.deleted, tc.wantDeleted) {
				t.Errorf("plex.tv requests %d, server deleted %v; want none and %v", r.tv.graphHits, r.pms.deleted, tc.wantDeleted)
			}
			assertContains(t, r.out.String(), tc.notice)
			recs := r.records(t)
			if len(recs) != 1 || recs[0].PlexTV != nil || !strings.Contains(recs[0].PlexTVSkipped, tc.notice) {
				t.Errorf("records %+v; want one with no plex.tv plays, saying why", recs)
			}
		})
	}
}

// Any other failure to find the owner stops the run: clearing the server
// alone would leave the owner's plex.tv history behind.
func TestClearHistory_PlexTV_OwnerLookupErrorStops(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*clearHistoryRun)
		want string
	}{
		"500":     {func(r *clearHistoryRun) { r.tv.userStatus = http.StatusInternalServerError }, "finding the PLEX_TOKEN owner"},
		"no uuid": {func(r *clearHistoryRun) { r.tv.uuid = "" }, "plex.tv gave no user id"},
	} {
		set, want := tc.set, tc.want
		t.Run(name, func(t *testing.T) {
			r := newCloudRun(t, "y")
			set(r)
			err := r.run(t, "QuienNZ", "Heat (1995)")
			if err == nil || !strings.HasSuffix(err.Error(), "nothing was deleted") || !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v; want %q, saying nothing was deleted", err, want)
			}
			r.assertUntouched(t)
		})
	}
}

// A plex.tv history that can't be read stops the run before anything is deleted.
func TestClearHistory_PlexTV_HistoryErrorDeletesNothing(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.historyErr = "internal error"
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "internal error") || !strings.HasSuffix(err.Error(), "nothing was deleted") {
		t.Errorf("err = %v; want plex.tv's error and that nothing was deleted", err)
	}
	r.assertUntouched(t)
}

// A rate-limited history read is waited out like a delete.
func TestClearHistory_PlexTV_HistoryRateLimitWaited(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.historyRateLimits = 1
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	if len(r.tv.removed) != 4 || !slices.Equal(r.slept, []time.Duration{time.Minute}) {
		t.Errorf("plex.tv removed %v after waiting %v; want 4 after a minute", r.tv.removed, r.slept)
	}
}

// A year-less target takes every year, with no notice for it; a movie plex.tv
// records no year for matches on title alone, and says so.
func TestClearHistory_PlexTV_Years(t *testing.T) {
	r := newCloudRun(t, "n")
	if err := r.run(t, "", "Heat", "The Thing (1982)"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	assertContains(t, out,
		planLine("Heat (1986)", 1, "2026-11-18T10:00:00.000Z"),
		planLine("Heat (1995)", 4, "2026-11-22T08:00:00.000Z"),
		planLine("The Thing", 1, "2026-11-17T10:00:00.000Z"),
		"plex.tv records no year for a movie these matched, so it matched on title alone: The Thing (1982)",
	)
	if strings.Contains(out, "Not deleting these") || strings.Contains(out, "more than one year") {
		t.Errorf("a year-less target left a year out, or flagged its years:\n%s", out)
	}
}

// A movie left for another year isn't listed as kept when another target
// takes it.
func TestClearHistory_PlexTV_OtherYearTakenByAnother(t *testing.T) {
	r := newCloudRun(t, "n")
	if err := r.run(t, "", "Heat (1995)", "Heat (1986)"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	assertContains(t, out, planLine("Heat (1986)", 1, "2026-11-18T10:00:00.000Z"))
	if strings.Contains(out, "Not deleting these") {
		t.Errorf("listed a taken movie as kept:\n%s", out)
	}
}

// A movie only plex.tv has is still cleared, and one it lacks is reported.
func TestClearHistory_PlexTV_MissingOnOneSide(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.history = append(r.tv.history, cloudPlay("c8", "Ronin", 1998, "2026-11-15T10:00:00.000Z"))
	if err := r.run(t, "", "Ronin", "Mission: Impossible – Fallout"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.tv.removed, []string{"c8"}) || !slices.Equal(r.pms.deleted, keys("5")) {
		t.Errorf("plex.tv removed %v, server deleted %v; want c8 and 5", r.tv.removed, r.pms.deleted)
	}
	assertContains(t, r.out.String(), "Not in plex.tv history:\n  Mission: Impossible – Fallout", "Not in this history:\n  Ronin")
}

// A movie only plex.tv has is asked about by its count alone.
func TestClearHistory_PlexTV_OnlyPlexTVMatches(t *testing.T) {
	r := newCloudRun(t, "n")
	r.tv.history = append(r.tv.history, cloudPlay("c8", "Ronin", 1998, "2026-11-15T10:00:00.000Z"))
	if err := r.run(t, "", "Ronin"); err != nil {
		t.Fatal(err)
	}
	assertContains(t, r.output(), "Delete these 0 server and 1 plex.tv history entries for QuienNZ (account 1)?")
}

// With only plex.tv's plays matched, the record lists the server's as [],
// as it does plex.tv's when only the server's match.
func TestClearHistory_PlexTV_OnlyPlexTVRecordsEmptyServer(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.history = append(r.tv.history, cloudPlay("c8", "Ronin", 1998, "2026-11-15T10:00:00.000Z"))
	if err := r.run(t, "", "Ronin"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(r.recordDir, "history-cleared-*.json"))
	if len(files) != 1 {
		t.Fatalf("records %v; want 1", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	if got := string(keys["server"]); got != "[]" {
		t.Errorf(`"server" is %s; want []`, got)
	}
}

// A rate limit is waited out, then the delete goes through.
func TestClearHistory_PlexTV_RateLimitWaited(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.rateLimits, r.tv.retryAfter = 2, 61
	if err := r.run(t, "", "Heat (1995)"); err != nil {
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
		if err := r.run(t, "", "Heat (1995)"); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.slept, []time.Duration{tc.want}) {
			t.Errorf("retryAfter %v: slept %v; want %v", tc.after, r.slept, tc.want)
		}
	}
}

// A delete still rate-limited after maxRateLimitTries tries fails, so each
// waits maxRateLimitTries-1 times; three such failures in a row stop the
// plex.tv deletes, and the record lists every play not deleted.
func TestClearHistory_PlexTV_RateLimitGivesUp(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.rateLimits = 1000
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), `4 history entries were not deleted`) || !strings.HasSuffix(err.Error(), `under "failedPlexTV"`) {
		t.Fatalf("err = %v; want 4 not deleted, under failedPlexTV", err)
	}
	if want := 3 * (maxRateLimitTries - 1); len(r.slept) != want {
		t.Errorf("slept %d times; want %d (%d tries for each of 3 plays)", len(r.slept), want, maxRateLimitTries)
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

// A plex.tv error on one play is reported, not retried, and the rest go ahead.
func TestClearHistory_PlexTV_OneFailure(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failRemove = map[string]bool{"c3": true}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "1 history entries were not deleted") {
		t.Fatalf("err = %v; want one not deleted", err)
	}
	if want := []string{"c1", "c2", "c4"}; !slices.Equal(r.tv.removed, want) {
		t.Errorf("plex.tv removed %v; want %v", r.tv.removed, want)
	}
	if len(r.slept) != 0 {
		t.Errorf("waited %v for an error that isn't a rate limit", r.slept)
	}
	assertContains(t, r.out.String(), "failed: plex.tv Heat (1995)"+watched("2026-11-20T10:00:00.000Z")+": plex.tv: removeActivity: boom")
	if strings.Contains(r.out.String(), "said it removed") {
		t.Errorf("the failed play is reported as one plex.tv said it removed:\n%s", r.out.String())
	}
}

// Failures on both sides are listed apart in the record.
func TestClearHistory_PlexTV_FailuresOnBothSides(t *testing.T) {
	r := newCloudRun(t, "y")
	r.pms.failKeys = map[string]bool{historyKeyPrefix + "1": true}
	r.tv.failRemove = map[string]bool{"c3": true}
	err := r.run(t, "", "Heat (1995)")
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

// Plays are grouped by plex.tv's movie id: a play that gives no year is its
// movie's, and a year-less movie beside one with the target's title and year
// is another movie, not taken.
func TestPlanCloud_ByMovieID(t *testing.T) {
	yearless := cloudPlay("a2", "Heat", 0, "")
	yearless.MovieID = movieID("Heat", 1995) // Heat (1995), with no year on this play
	other := cloudPlay("b1", "Heat", 0, "")  // another movie, with no year
	plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), yearless, other}, "Heat (1995)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "a2"}) {
		t.Errorf("matched %v; want a1 and a2", got)
	}
	if len(plan.movies) != 1 || len(plan.yearUnchecked) != 0 || !slices.Equal(plan.yearless, []string{"Heat"}) {
		t.Errorf("movies %v, unchecked %v, kept year-less %v; want one movie, checked, and Heat kept", plan.movies, plan.yearUnchecked, plan.yearless)
	}
}

// A year-less play with another year's movie id is that movie: a dated
// target out of its range leaves it, as another year, not as year-less.
func TestPlanCloud_YearlessPlayOfOtherYear(t *testing.T) {
	yearless := cloudPlay("b2", "Heat", 0, "")
	yearless.MovieID = movieID("Heat", 1986)
	plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), cloudPlay("b1", "Heat", 1986, ""), yearless}, "Heat (1995)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1"}) {
		t.Errorf("matched %v; want only a1", got)
	}
	if !slices.Equal(plan.otherYears, []string{"Heat (1986)"}) || plan.yearless != nil || plan.yearUnchecked != nil {
		t.Errorf("other years %v, kept year-less %v, unchecked %v; want Heat (1986) as another year only", plan.otherYears, plan.yearless, plan.yearUnchecked)
	}
}

// A target's year matches a movie's within one, as on the server; further
// out it's another year.
func TestPlanCloud_YearWithinOne(t *testing.T) {
	entries := []cloudEntry{cloudPlay("a1", "Heat", 1995, "")}
	for target, take := range map[string]bool{"Heat (1994)": true, "Heat (1995)": true, "Heat (1996)": true, "Heat (1993)": false, "Heat (1997)": false} {
		plan := planCloudFor(t, entries, target)
		if got := len(plan.matched) == 1; got != take {
			t.Errorf("%s: took Heat (1995): %v; want %v", target, got, take)
		}
		if !take && !slices.Equal(plan.otherYears, []string{"Heat (1995)"}) {
			t.Errorf("%s: other years %v; want Heat (1995)", target, plan.otherYears)
		}
	}
}

// A dated target whose range takes movies of two years says so, since they
// may be different movies; a year-less target doesn't.
func TestPlanCloud_SeveralYears(t *testing.T) {
	entries := []cloudEntry{cloudPlay("a1", "Ghost", 1990, ""), cloudPlay("b1", "Ghost", 1991, "")}
	plan := planCloudFor(t, entries, "Ghost (1990)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "b1"}) || !slices.Equal(plan.severalYears, []string{"Ghost (1990)"}) {
		t.Errorf("matched %v, several years %v; want both, flagged", got, plan.severalYears)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "These matched plex.tv movies of more than one year, which may be different movies: check each before answering: Ghost (1990)")
	if plan := planCloudFor(t, entries, "Ghost"); plan.severalYears != nil {
		t.Errorf("a year-less target flagged %v", plan.severalYears)
	}
	// 1989's range reaches 1990 only.
	if plan := planCloudFor(t, entries, "Ghost (1989)"); len(plan.matched) != 1 || plan.severalYears != nil {
		t.Errorf("Ghost (1989): matched %v, several years %v; want one movie, unflagged", matchedIDs(plan), plan.severalYears)
	}
}

// A movie's year is the first any of its plays gives, so a first play
// without one doesn't make it match on title alone.
func TestPlanCloud_YearFromLaterPlay(t *testing.T) {
	first := cloudPlay("a1", "Heat", 0, "")
	first.MovieID = movieID("Heat", 1995)
	plan := planCloudFor(t, []cloudEntry{first, cloudPlay("a2", "Heat", 1995, ""), cloudPlay("b1", "Heat", 1986, "")}, "Heat (1995)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "a2"}) || plan.yearUnchecked != nil {
		t.Errorf("matched %v, year unchecked %v; want a1 and a2, checked", got, plan.yearUnchecked)
	}
	for _, sum := range plan.movies {
		if sum.movie.year != 1995 {
			t.Errorf("movie %v; want its year 1995", sum.movie)
		}
	}
}

// Two movies with the same title and year are told apart by id, and flagged.
func TestPlanCloud_SameTitleAndYear(t *testing.T) {
	a := cloudPlay("a1", "Heat", 1995, "")
	b := cloudPlay("b1", "Heat", 1995, "")
	b.MovieID = "mv-other"
	plan := planCloudFor(t, []cloudEntry{a, b}, "Heat (1995)")
	if len(plan.movies) != 2 {
		t.Fatalf("movies %v; want 2", plan.movies)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "Heat (1995) [mv-Heat-1995]", "Heat (1995) [mv-other]", "may be different movies")
}

// A year plex.tv gives in the title alone is the movie's year, shown once.
func TestPlanCloud_YearInTitle(t *testing.T) {
	e := cloudPlay("a1", "Solaris (2002)", 0, "")
	plan := planCloudFor(t, []cloudEntry{e}, "Solaris (2002)")
	if !slices.Equal(matchedIDs(plan), []string{"a1"}) || len(plan.yearUnchecked) != 0 {
		t.Fatalf("matched %v, unchecked %v; want a1, checked", matchedIDs(plan), plan.yearUnchecked)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	if s := out.String(); !strings.Contains(s, "Solaris (2002) ") || strings.Contains(s, "(2002) (2002)") {
		t.Errorf("output:\n%s", s)
	}
}

// The play watched last is found by time, not by text: ".500Z" sorts before
// "Z" but is later.
func TestPlanCloud_LastWatchedByTime(t *testing.T) {
	plan := planCloudFor(t, []cloudEntry{
		cloudPlay("a1", "Heat", 1995, "2026-11-22T08:00:00.500Z"),
		cloudPlay("a2", "Heat", 1995, "2026-11-22T08:00:00Z"),
	}, "Heat")
	if len(plan.movies) != 1 {
		t.Fatalf("movies %v; want 1", plan.movies)
	}
	for _, sum := range plan.movies {
		if sum.last.ID != "a1" {
			t.Errorf("last watched %s; want a1", sum.last.ID)
		}
	}
}

// plex.tv's reply to a removal isn't trusted: the history is read again, a
// second time after a wait when it still lists them, and each play still
// listed is reported, though plex.tv said it was removed.
func TestClearHistory_PlexTV_ClaimedButStillListed(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.ignoreRemove = true
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.HasSuffix(err.Error(), `under "failedPlexTV"`) {
		t.Fatalf("err = %v; want the plays listed under failedPlexTV", err)
	}
	assertContains(t, r.out.String(), "plex.tv still lists 4 play(s) it was asked to remove; checking again in 10s.",
		"plex.tv still lists 4 of the plays it said it removed:\n",
		"  still listed: plex.tv Heat (1995)"+watched("2026-11-21T10:00:00.000Z")+"\n",
		"  still listed: plex.tv Heat (1995)"+watched("2026-11-19T10:00:00.000Z")+"\n",
		"Deleted 2 of 2 server and 0 of 4 plex.tv history entries.")
	if strings.Contains(r.out.String(), "no longer lists") {
		t.Errorf("output reports plays gone:\n%s", r.out.String())
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
	if err := r.run(t, "", "Heat (1995)"); err != nil {
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
// play is tried.
func TestClearHistory_PlexTV_RemovedDespiteReply(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.removeResult = nil // plex.tv removes the play but replies null
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"c1", "c2", "c3", "c4"}; !slices.Equal(r.tv.removed, want) {
		t.Errorf("plex.tv was asked to remove %v; want %v", r.tv.removed, want)
	}
	assertContains(t, r.out.String(), "  unconfirmed: plex.tv Heat (1995)"+watched("2026-11-20T10:00:00.000Z")+":",
		"plex.tv no longer lists 4 of the plays whose removal it didn't confirm.",
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
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "4 history entries were not deleted") {
		t.Fatalf("err = %v; want all 4 not deleted", err)
	}
	if len(r.tv.removed) != 4 || !slices.Equal(r.slept, []time.Duration{settleWait}) {
		t.Errorf("asked to remove %v, waited %v; want all 4 tried and one settle wait", r.tv.removed, r.slept)
	}
	if strings.Contains(r.out.String(), "said it removed") {
		t.Errorf("an unconfirmed removal is reported as one plex.tv said it made:\n%s", r.out.String())
	}
	assertContains(t, r.out.String(), "plex.tv still lists 4 of the plays whose removal it didn't confirm:\n",
		"  still listed: plex.tv Heat (1995)"+watched("2026-11-19T10:00:00.000Z"))
}

// A play whose removal failed with an error stays failed though plex.tv no
// longer lists it: only a reply that doesn't say is overruled.
func TestClearHistory_PlexTV_ErrorNotOverruled(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failRemove = map[string]bool{"c3": true}
	r.tv.onRemove = func() {
		if len(r.tv.removed) == 2 { // c3 goes, and its removal fails anyway
			r.tv.history = slices.DeleteFunc(r.tv.history, func(e cloudEntry) bool { return e.ID == "c3" })
		}
	}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "1 history entries were not deleted") {
		t.Fatalf("err = %v; want c3 not deleted", err)
	}
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c3"}) || recs[0].PlexTVUnchecked != "" {
		t.Errorf("records %+v; want c3 failed, checked", recs)
	}
	if len(r.slept) != 0 {
		t.Errorf("waited %v for a play whose removal failed with an error", r.slept)
	}
}

// Some removed, one claimed but still listed and one failed: each is
// reported for what it is.
func TestClearHistory_PlexTV_MixedOutcome(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.failRemove, r.tv.keepListed = map[string]bool{"c3": true}, map[string]bool{"c2": true}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "2 history entries were not deleted") {
		t.Fatalf("err = %v; want c2 and c3 not deleted", err)
	}
	assertContains(t, r.out.String(), "plex.tv still lists 1 play(s) it was asked to remove; checking again in 10s.",
		"plex.tv still lists 1 of the plays it said it removed:\n  still listed: plex.tv Heat (1995)"+watched("2026-11-22T08:00:00.000Z")+"\n",
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
	err := r.run(t, "", "Heat (1995)")
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
	err := r.run(t, "", "Heat (1995)")
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
			func(cloudEntry) bool { return true }, []string{"c1", "c2", "c3", "c4"}, "lacks 4 play(s)"},
		{"an untried play gone", map[string]bool{"c1": true, "c2": true, "c3": true},
			func(e cloudEntry) bool { return e.ID == "c4" }, []string{"c1", "c2", "c3", "c4"}, "lacks 1 play(s)"},
		{"an unmatched item gone", nil,
			func(e cloudEntry) bool { return e.ID == "c5" }, nil, "lacks 1 play(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newCloudRun(t, "y")
			r.tv.failRemove = tc.fail
			r.tv.onRemove = func() { r.tv.history = slices.DeleteFunc(r.tv.history, tc.lose) }
			err := r.run(t, "", "Heat (1995)")
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
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.slept, []time.Duration{time.Minute}) {
		t.Errorf("waited %v; want a minute for the re-read", r.slept)
	}
}

// Matching plays the plan didn't have (watched mid-run, or paged into view)
// are reported for another run, not deleted.
func TestClearHistory_PlexTV_NewMatchesReported(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.onRemove = func() {
		if !slices.ContainsFunc(r.tv.history, func(e cloudEntry) bool { return e.ID == "c9" }) {
			r.tv.history = append(r.tv.history, cloudPlay("c9", "Heat", 1995, "2026-11-22T09:00:00.000Z"))
		}
	}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "plex.tv now lists 1 more matching play(s); run again to clear them") {
		t.Fatalf("err = %v; want the new play reported", err)
	}
	assertContains(t, r.out.String(), "plex.tv now lists 1 play(s) of these movies that the plan didn't have; run again to clear them.")
	if slices.Contains(r.tv.removed, "c9") {
		t.Error("removed the play the plan didn't have")
	}
	if recs := r.records(t); len(recs) != 1 || recs[0].FailedPlexTV != nil {
		t.Errorf("records %+v; want nothing failed", recs)
	}
}

// When the record can't be rewritten, the plays plex.tv still lists are
// named in the output, and the error says so beside the rewrite failure.
func TestClearHistory_PlexTV_RecordUpdateFails(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	r := newCloudRun(t, "y")
	r.blockRecordOnDelete(t)
	r.tv.keepListed = map[string]bool{"c2": true}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "couldn't be updated to say so") ||
		!strings.Contains(err.Error(), "1 history entries were not deleted; the output above names them") {
		t.Fatalf("err = %v; want the rewrite failure and the play not deleted", err)
	}
	assertContains(t, r.out.String(), "  still listed: plex.tv Heat (1995)"+watched("2026-11-22T08:00:00.000Z"))
}

// A history read still rate-limited after every try stops the run before
// anything is deleted.
func TestClearHistory_PlexTV_HistoryRateLimitGivesUp(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.historyRateLimits = 1000
	err := r.run(t, "", "Heat (1995)")
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

// A play with plex.tv's movie id but no title goes with the movie's titled
// plays; movie items naming no movie at all are counted and left alone, and
// other items aren't movies.
func TestPlanCloud_ItemsWithoutTitle(t *testing.T) {
	untitled := cloudPlay("a2", "", 0, "")
	untitled.MovieID = movieID("Heat", 1995)
	plan := planCloudFor(t, []cloudEntry{
		untitled, cloudPlay("a1", "Heat", 1995, ""), // the title comes from a later play
		{ID: "n1", Type: cloudTypeMovie}, // no movie at all
		{ID: "n2"},                       // plex.tv sent no item
		{ID: "n3", Type: "EPISODE", Title: "Heat"},
		{ID: "n4", Type: cloudTypeMovie, MovieID: "untitled"}, // a movie id no play titles
	}, "Heat (1995)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a2", "a1"}) {
		t.Errorf("matched %v; want a2 and a1", got)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "plex.tv lists 3 play(s) that name no movie to match, so they're left alone.")
}

// A movie plex.tv gives no id or year beside one in the target's range is
// another movie, so it's left, and the plan says so.
func TestPlanCloud_YearlessBesideTargetYear(t *testing.T) {
	lost := cloudPlay("b1", "Heat", 0, "")
	lost.MovieID = ""
	for _, target := range []string{"Heat (1995)", "Heat (1996)"} {
		plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), lost}, target)
		if got := matchedIDs(plan); !slices.Equal(got, []string{"a1"}) || plan.yearUnchecked != nil {
			t.Errorf("%s: matched %v, year unchecked %v; want only a1", target, got, plan.yearUnchecked)
		}
		var out strings.Builder
		printCloudPlan(&out, "QuienNZ", plan)
		assertContains(t, out.String(), "Not deleting these, which plex.tv gives no year while the title is there within a year of the one asked for (give the title without its year to include them): Heat")
	}
}

// A year-less movie beside only another year's matches on title alone: the
// other year's movie can't be the target, the year-less one can.
func TestPlanCloud_YearlessBesideAnotherYear(t *testing.T) {
	lost := cloudPlay("b1", "Heat", 0, "")
	lost.MovieID = ""
	plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), lost}, "Heat (1986)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"b1"}) || !slices.Equal(plan.yearUnchecked, []string{"Heat (1986)"}) {
		t.Errorf("matched %v, year unchecked %v; want b1 on title alone", got, plan.yearUnchecked)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "Not deleting these, which have another year: Heat (1995)",
		"plex.tv records no year for a movie these matched, so it matched on title alone: Heat (1986)")
	if strings.Contains(out.String(), "gives no year while") {
		t.Errorf("the year-less movie is listed as kept:\n%s", out.String())
	}
}

// plex.tv titles match as server titles do: case, punctuation and full
// width folded.
func TestPlanCloud_TitleFolding(t *testing.T) {
	plan := planCloudFor(t, []cloudEntry{
		cloudPlay("a1", "Mission Impossible", 1996, ""),
		cloudPlay("b1", "ＡＫＩＲＡ", 1988, ""),
	}, "mission: impossible", "Akira (1988)")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "b1"}) {
		t.Errorf("matched %v; want both", got)
	}
}

// Dates show in local time, as the server's plan does.
func TestDescribeCloudItem_LocalDate(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("NZDT", 13*60*60)
	t.Cleanup(func() { time.Local = saved })
	e := cloudPlay("a1", "Heat", 1995, "2026-11-17T20:00:00Z")
	if got := describeCloudItem(e); got != ", last watched 2026-11-18" {
		t.Errorf("describeCloudItem = %q; want the NZ date 2026-11-18", got)
	}
}

// A date that can't be read isn't printed, and doesn't hide a readable later one.
func TestPlanCloud_UnreadableDate(t *testing.T) {
	plan := planCloudFor(t, []cloudEntry{
		cloudPlay("a1", "Heat", 1995, "1767225600"),
		cloudPlay("a2", "Heat", 1995, "2026-11-22T08:00:00Z"),
	}, "Heat")
	if len(plan.movies) != 1 {
		t.Fatalf("movies %v; want 1", plan.movies)
	}
	for _, sum := range plan.movies {
		if sum.last.ID != "a2" {
			t.Errorf("last watched %s; want a2", sum.last.ID)
		}
	}
	if got := describeCloudItem(cloudPlay("a1", "Heat", 1995, "1767225600")); got != "" {
		t.Errorf("describeCloudItem = %q; want no date", got)
	}
}

// Failure lines name a movie whose title carries its year once.
func TestCloudPlanDescribe_YearInTitle(t *testing.T) {
	e := cloudPlay("a1", "Solaris (2002)", 2002, "")
	if got := planCloudFor(t, []cloudEntry{e}, "Solaris").describe(e); got != "plex.tv Solaris (2002)" {
		t.Errorf("describe = %q", got)
	}
}

// A play without its movie's title is named by the movie it was grouped with.
func TestCloudPlanDescribe_UntitledItem(t *testing.T) {
	untitled := cloudPlay("a2", "", 0, "")
	untitled.MovieID = movieID("Heat", 1995)
	plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), untitled}, "Heat")
	if got := plan.describe(untitled); got != "plex.tv Heat (1995)" {
		t.Errorf("describe = %q; want the grouped movie's name", got)
	}
}

// A year-less movie the plan kept beside the target's year stays kept when
// the run checks for new plays, though the dated movie's plays are gone.
func TestClearHistory_PlexTV_KeptYearlessNotNew(t *testing.T) {
	r := newCloudRun(t, "y")
	other := cloudPlay("b1", "Heat", 0, "2026-11-15T10:00:00.000Z")
	other.MovieID = ""
	r.tv.history = append(r.tv.history, other)
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.out.String(), "didn't have") || slices.Contains(r.tv.removed, "b1") {
		t.Errorf("the kept movie is reported new or removed:\n%s", r.out.String())
	}
}

// A play of that kept movie watched during the run isn't reported either:
// planned with the first read, its movie is still beside the target's year.
func TestClearHistory_PlexTV_KeptYearlessNewPlayNotNew(t *testing.T) {
	r := newCloudRun(t, "y")
	other := cloudPlay("b1", "Heat", 0, "2026-11-15T10:00:00.000Z")
	other.MovieID = ""
	r.tv.history = append(r.tv.history, other)
	r.tv.onRemove = func() {
		if !slices.ContainsFunc(r.tv.history, func(e cloudEntry) bool { return e.ID == "b2" }) {
			play := cloudPlay("b2", "Heat", 0, "2026-11-22T11:00:00.000Z")
			play.MovieID = ""
			r.tv.history = append([]cloudEntry{play}, r.tv.history...)
		}
	}
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.out.String(), "didn't have") {
		t.Errorf("the kept movie's new play is reported:\n%s", r.out.String())
	}
}

// With nothing after a play in the first read still listed, the re-read
// can't show it went: an unconfirmed removal of it stays failed, a confirmed
// one stands, and the output says it's by the reply.
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
			r.tv.history = r.tv.history[:4] // only the target movie
			r.tv.removeResult, r.tv.ignoreRemove = tc.reply, true
			r.tv.onRemove = func() { r.tv.history = nil } // the re-read comes back empty
			err := r.run(t, "", "Heat (1995)")
			recs := r.records(t)
			if len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, tc.wantFailed) || recs[0].PlexTVUnchecked != "" {
				t.Fatalf("records %+v; want failed %v, checked", recs, tc.wantFailed)
			}
			if tc.wantFailed == nil {
				if err != nil {
					t.Errorf("err = %v; want none", err)
				}
				assertContains(t, r.out.String(), "The last read of plex.tv lists nothing after 4 of the plays it said it removed, so those count as deleted by its replies.")
				return
			}
			if err == nil || !strings.Contains(err.Error(), "4 history entries were not deleted") {
				t.Errorf("err = %v; want all 4 not deleted", err)
			}
			assertContains(t, r.out.String(), "The last read of plex.tv lists nothing after 4 of the plays whose removal it didn't confirm")
			if strings.Contains(r.out.String(), "by its replies") {
				t.Errorf("unconfirmed plays are counted by their replies:\n%s", r.out.String())
			}
			if strings.Contains(r.out.String(), "no longer lists") {
				t.Errorf("output takes the empty read as removals:\n%s", r.out.String())
			}
		})
	}
}

// When the read after the wait fails or comes back short, the first re-read,
// which showed the plays still there, stands.
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
			err := r.run(t, "", "Heat (1995)")
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

// When the record can't be rewritten, plays not tried are named too.
func TestClearHistory_PlexTV_RecordUpdateFailsNamesUntried(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	r := newCloudRun(t, "y")
	r.blockRecordOnDelete(t)
	r.tv.failRemove = map[string]bool{"c1": true, "c2": true, "c3": true}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "4 history entries were not deleted; the output above names them") {
		t.Fatalf("err = %v; want 4 not deleted, named above", err)
	}
	assertContains(t, r.out.String(), "Not deleted:\n", "\n  plex.tv Heat (1995)"+watched("2026-11-19T10:00:00.000Z")+"\n")
}

// Unconfirmed replies are plex.tv answering, so they break a run of failures.
func TestDeleteEach_UnconfirmedBreaksRun(t *testing.T) {
	boom, odd := errors.New("boom"), &unconfirmedError{msg: "odd"}
	errs := []error{boom, boom, odd, boom, boom, nil}
	var tried []string
	ids := []string{"a", "b", "c", "d", "e", "f"}
	res := deleteEach(io.Discard, "plex.tv", ids, func(s string) string { return s }, func(s string) string { return s },
		func(id string) error { tried = append(tried, id); return errs[len(tried)-1] })
	if !slices.Equal(tried, ids) || len(res.untried) != 0 || !res.unconfirmed["c"] {
		t.Errorf("tried %v, untried %v, unconfirmed %v; want all tried and c unconfirmed", tried, res.untried, res.unconfirmed)
	}
}

// A reply cut off is unconfirmed, but may be plex.tv going away, so it
// counts toward a run of failures.
func TestDeleteEach_CutOffCountsTowardStop(t *testing.T) {
	boom, cut := errors.New("boom"), &unconfirmedError{msg: "cut off", cut: true}
	errs := []error{boom, boom, cut, nil, nil}
	var tried []string
	ids := []string{"a", "b", "c", "d", "e"}
	res := deleteEach(io.Discard, "plex.tv", ids, func(s string) string { return s }, func(s string) string { return s },
		func(id string) error { tried = append(tried, id); return errs[len(tried)-1] })
	if !slices.Equal(tried, ids[:3]) || !res.untried["d"] || !res.untried["e"] || !res.unconfirmed["c"] {
		t.Errorf("tried %v, untried %v, unconfirmed %v; want a–c tried, c unconfirmed, d and e untried", tried, res.untried, res.unconfirmed)
	}
}

// A removal that took, whose reply was cut off, counts once the re-read
// shows it gone.
func TestClearHistory_PlexTV_CutOffReplyRecovered(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.cutRemove = map[string]bool{"c2": true}
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	assertContains(t, r.out.String(), "  failed: plex.tv Heat (1995)"+watched("2026-11-22T08:00:00.000Z")+": plex.tv: removeActivity: the reply was cut off",
		"plex.tv no longer lists 1 of the plays whose removal it didn't confirm.",
		"Deleted 2 of 2 server and 4 of 4 plex.tv history entries.")
	if strings.Contains(r.out.String(), "  unconfirmed:") {
		t.Errorf("the cut-off reply, a failure toward the stop, is labelled unconfirmed:\n%s", r.out.String())
	}
	if recs := r.records(t); len(recs) != 1 || recs[0].FailedPlexTV != nil {
		t.Errorf("records %+v; want nothing failed", recs)
	}
}

// The target movie's plays are the oldest in the history and plex.tv shows
// the removals only after the wait: nothing after them is left to show they
// went, so they count by plex.tv's true replies, and the output says so.
func TestClearHistory_PlexTV_OldestMovieSettles(t *testing.T) {
	r := newCloudRun(t, "y")
	h := r.tv.history
	r.tv.history = append(slices.Clone(h[4:]), h[:4]...)
	r.tv.ignoreRemove = true
	r.onSleep = func() {
		r.tv.history = slices.DeleteFunc(r.tv.history, func(e cloudEntry) bool { return slices.Contains(r.tv.removed, e.ID) })
	}
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	assertContains(t, r.out.String(), "plex.tv still lists 4 play(s) it was asked to remove; checking again in 10s.",
		"The last read of plex.tv lists nothing after 4 of the plays it said it removed, so those count as deleted by its replies.",
		"Deleted 2 of 2 server and 4 of 4 plex.tv history entries.")
	if recs := r.records(t); len(recs) != 1 || recs[0].FailedPlexTV != nil {
		t.Errorf("records %+v; want nothing failed", recs)
	}
}

// Only the last read decides how far it reached: the first, read while
// removals were landing, may have skipped a play, so what it showed past
// isn't kept once the read after the wait no longer lists the plays that
// showed it.
func TestClearHistory_PlexTV_LastReadDecidesReach(t *testing.T) {
	r := newCloudRun(t, "y")
	h := r.tv.history
	r.tv.history = append(slices.Clone(h[4:]), h[:4]...) // c5 c6 c7 c1 c2 c3 c4
	r.tv.cutRemove = map[string]bool{"c1": true}         // it goes, but its reply is cut off
	r.tv.keepListed = map[string]bool{"c4": true}        // listed by the first read only
	r.onSleep = func() {
		r.tv.history = slices.DeleteFunc(r.tv.history, func(e cloudEntry) bool { return e.ID == "c4" })
	}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "1 history entries were not deleted") {
		t.Fatalf("err = %v; want c1 not deleted\n%s", err, r.out.String())
	}
	assertContains(t, r.out.String(), "The last read of plex.tv lists nothing after 1 of the plays whose removal it didn't confirm",
		"The last read of plex.tv lists nothing after 3 of the plays it said it removed, so those count as deleted by its replies.",
		"Deleted 2 of 2 server and 3 of 4 plex.tv history entries.")
	if strings.Contains(r.out.String(), "no longer lists") {
		t.Errorf("output takes the first read's reach:\n%s", r.out.String())
	}
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c1"}) {
		t.Errorf("records %+v; want c1 failed", recs)
	}
}

// A play to be removed that's still listed shows the re-read reached past
// the plays before it, so their unconfirmed removals count once they're gone.
func TestClearHistory_PlexTV_ListedItemShowsReach(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.history = r.tv.history[:4] // only the target movie
	r.tv.removeResult, r.tv.keepListed = nil, map[string]bool{"c4": true}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "1 history entries were not deleted") {
		t.Fatalf("err = %v; want only c4 not deleted", err)
	}
	assertContains(t, r.out.String(), "plex.tv no longer lists 3 of the plays whose removal it didn't confirm.",
		"  still listed: plex.tv Heat (1995)"+watched("2026-11-19T10:00:00.000Z"))
	if strings.Contains(r.out.String(), "The last read of plex.tv lists nothing after") {
		t.Errorf("output says the re-read didn't reach them:\n%s", r.out.String())
	}
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(recs[0].FailedPlexTV, []string{"c4"}) {
		t.Errorf("records %+v; want c4 failed", recs)
	}
}

// A play the first read gave no title, which the re-read titles as a target
// movie, is reported for another run: the re-read's titles win.
func TestClearHistory_PlexTV_RereadTitlesWin(t *testing.T) {
	r := newCloudRun(t, "y")
	untitled := cloudPlay("c8", "", 0, "2026-11-15T10:00:00.000Z")
	untitled.MovieID = "mv-other"
	r.tv.history = append(r.tv.history, untitled)
	r.tv.onRemove = func() {
		for i := range r.tv.history {
			if r.tv.history[i].ID == "c8" {
				r.tv.history[i].Title, r.tv.history[i].Year = "Heat", 1995
			}
		}
	}
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "plex.tv now lists 1 more matching play(s)") {
		t.Fatalf("err = %v; want c8 reported", err)
	}
	if slices.Contains(r.tv.removed, "c8") {
		t.Error("removed the play the plan didn't have")
	}
}

// A target with a year and one without, of one title: the year-less one
// takes the year-less movie, which isn't then listed as kept.
func TestPlanCloud_DatedAndUndatedTargets(t *testing.T) {
	other := cloudPlay("b1", "Heat", 0, "")
	other.MovieID = ""
	plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), other}, "Heat (1995)", "Heat")
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "b1"}) || plan.yearless != nil || plan.yearUnchecked != nil {
		t.Errorf("matched %v, kept %v, unchecked %v; want both matched, nothing kept or unchecked", got, plan.yearless, plan.yearUnchecked)
	}
}

// A play of a title asked for that plex.tv doesn't type MOVIE isn't taken,
// but the plan names it rather than only calling the title missing. Other
// types' plays of other titles, and episodes and shows, aren't named.
func TestPlanCloud_OtherTypesNamed(t *testing.T) {
	clip := cloudEntry{ID: "c1", Type: "CLIP", Title: "Heat"}
	lower := cloudEntry{ID: "c2", Type: "movie", Title: "Heat (1995)"}
	plan := planCloudFor(t, []cloudEntry{clip, lower, clip,
		{ID: "c3", Type: "CLIP", Title: "Alien"},
		{ID: "c4", Type: "EPISODE", Title: "Heat"},
		{ID: "c5", Type: "SHOW", Title: "Heat"},
		{ID: "c6", Type: "SEASON", Title: "Heat"},
		{ID: "c7", Type: "Episode", Title: "Heat"},
		{ID: "c8", Title: "Heat"}, // no type
		{ID: "c9", Type: "CLIP"},  // names nothing: counted
		{ID: "c10", Type: "SHOW"}, // TV: not counted
	}, "Heat (1995)")
	if len(plan.matched) != 0 || !slices.Equal(plan.missing, []string{"Heat (1995)"}) {
		t.Errorf("matched %v, missing %v; want nothing taken", matchedIDs(plan), plan.missing)
	}
	if want := []string{"Heat (1995) [movie]", "Heat [CLIP]", "Heat [no type]"}; !slices.Equal(plan.notMovies, want) {
		t.Errorf("not movies %q; want %q", plan.notMovies, want)
	}
	if plan.titleless != 1 {
		t.Errorf("titleless %d; want 1, the untitled clip", plan.titleless)
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	assertContains(t, out.String(), "Not deleting these plays of a title asked for, whose plex.tv type isn't MOVIE: Heat (1995) [movie], Heat [CLIP], Heat [no type]")
}

// Plays without a movie id are different movies when their years differ.
func TestPlanCloud_IDlessYearsApart(t *testing.T) {
	a := cloudEntry{ID: "a1", Type: cloudTypeMovie, Title: "Heat", Year: 1995}
	b := cloudEntry{ID: "b1", Type: cloudTypeMovie, Title: "Heat (1986)"}
	for _, entries := range [][]cloudEntry{{a, b}, {b, a}} {
		plan := planCloudFor(t, entries, "Heat (1995)")
		if got := matchedIDs(plan); !slices.Equal(got, []string{"a1"}) || !slices.Equal(plan.otherYears, []string{"Heat (1986)"}) {
			t.Errorf("matched %v, other years %v; want a1, and Heat (1986) kept", got, plan.otherYears)
		}
	}
}

// A play without a movie id is grouped on the title and year it names, so
// a year in its title and one plex.tv gives are the same movie.
func TestPlanCloud_IDlessGroupedByParsedTitle(t *testing.T) {
	a := cloudEntry{ID: "a1", Type: cloudTypeMovie, Title: "Heat (1995)"}
	b := cloudEntry{ID: "a2", Type: cloudTypeMovie, Title: "Heat", Year: 1995}
	plan := planCloudFor(t, []cloudEntry{a, b}, "Heat (1995)")
	if len(plan.matched) != 2 || len(plan.movies) != 1 {
		t.Fatalf("matched %v in %d movie(s); want both in one", matchedIDs(plan), len(plan.movies))
	}
	var out strings.Builder
	printCloudPlan(&out, "QuienNZ", plan)
	if s := out.String(); strings.Contains(s, "[no id]") || strings.Contains(s, "may be different movies") {
		t.Errorf("one movie shown as two:\n%s", s)
	}
}

// Kept movies that print alike are each listed, told apart by id.
func TestPlanCloud_KeptMoviesToldApart(t *testing.T) {
	other := cloudPlay("b1", "Heat", 1986, "")
	other.MovieID = "mv-other"
	plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), cloudPlay("c1", "Heat", 1986, ""), other}, "Heat (1995)")
	if want := []string{"Heat (1986) [mv-Heat-1986]", "Heat (1986) [mv-other]"}; !slices.Equal(plan.otherYears, want) {
		t.Errorf("other years %q; want %q", plan.otherYears, want)
	}
	if plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), cloudPlay("c1", "Heat", 1986, "")}, "Heat (1995)"); !slices.Equal(plan.otherYears, []string{"Heat (1986)"}) {
		t.Errorf("other years %q; want Heat (1986) without an id", plan.otherYears)
	}
	idless := cloudPlay("d1", "Heat", 1986, "")
	idless.MovieID = ""
	if plan := planCloudFor(t, []cloudEntry{cloudPlay("a1", "Heat", 1995, ""), cloudPlay("c1", "Heat", 1986, ""), idless}, "Heat (1995)"); !slices.Equal(plan.otherYears, []string{"Heat (1986) [mv-Heat-1986]", "Heat (1986) [no id]"}) {
		t.Errorf("other years %q; want the id-less one labelled [no id]", plan.otherYears)
	}
}

// A target given twice is listed once, as missing and as taking several years.
func TestPlanCloud_RepeatedTarget(t *testing.T) {
	entries := []cloudEntry{cloudPlay("a1", "Ghost", 1990, ""), cloudPlay("b1", "Ghost", 1991, "")}
	plan := planCloudFor(t, entries, "Ghost (1990)", "Ghost (1990)", "Alien", "Alien")
	if !slices.Equal(plan.missing, []string{"Alien"}) || !slices.Equal(plan.severalYears, []string{"Ghost (1990)"}) {
		t.Errorf("missing %v, several years %v; want each once", plan.missing, plan.severalYears)
	}
	if got := matchedIDs(plan); !slices.Equal(got, []string{"a1", "b1"}) {
		t.Errorf("matched %v; want each play once", got)
	}
}

// plex.tv's year for a movie wins over a "(Year)" in its title.
func TestCloudMovieOf_YearPrecedence(t *testing.T) {
	if m := cloudMovieOf(cloudEntry{Title: "Heat (1986)", Year: 1995}); m.title != "Heat" || m.year != 1995 {
		t.Errorf("movie %+v; want Heat, 1995", m)
	}
	if m := cloudMovieOf(cloudEntry{Title: "Heat (1986)"}); m.title != "Heat" || m.year != 1986 {
		t.Errorf("movie %+v; want Heat, 1986", m)
	}
}
