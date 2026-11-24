package main

import (
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

// The owner's plex.tv items of a target show go with the server's: episodes,
// seasons and the show, but not another year's show or a movie.
func TestClearHistory_PlexTV(t *testing.T) {
	r := newCloudRun(t, "y")
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	if want := keys("1", "2"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("server deleted %v; want %v", r.pms.deleted, want)
	}
	if want := []string{"c1", "c2", "c3", "c4"}; !slices.Equal(r.tv.removed, want) {
		t.Errorf("plex.tv removed %v; want %v", r.tv.removed, want)
	}
	out := r.out.String()
	for _, want := range []string{
		"Watch history on plex.tv:",
		`Bluey (2018)                             2 episode(s), 2 season or show item(s), last watched S02E05 "Hospital" on 2026-11-22`,
		"Not deleting these, which have another year: Bluey (1992)",
		"Delete these 2 server and 4 plex.tv history entries for QuienNZ (account 1)?",
		"Deleted 6 of 6 history entries.",
	} {
		if !strings.Contains(out+strings.Join(r.prompter.asked, ""), want) {
			t.Errorf("output lacks %q:\n%s%s", want, out, strings.Join(r.prompter.asked, ""))
		}
	}
	recs := r.records(t)
	if len(recs) != 1 || len(recs[0].PlexTV) != 4 || recs[0].PlexTV[0] != r.tv.history[0] || !recs[0].Complete {
		t.Errorf("records %+v; want one, complete, with the 4 plex.tv items", recs)
	}
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

// Another account's run leaves plex.tv alone, and says so.
func TestClearHistory_PlexTV_OtherAccountSkipped(t *testing.T) {
	r := newCloudRun(t, "y")
	if err := r.run(t, "Stephanie", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if r.tv.graphHits != 0 {
		t.Errorf("read or changed plex.tv %d times for another account", r.tv.graphHits)
	}
	if !slices.Equal(r.pms.deleted, keys("9")) {
		t.Errorf("server deleted %v; want Stephanie's Bluey", r.pms.deleted)
	}
	if !strings.Contains(r.out.String(), "Stephanie's there isn't cleared") {
		t.Errorf("no skip notice:\n%s", r.out.String())
	}
}

// With --account, a failed owner lookup (a server token's 401) skips plex.tv
// and still clears the server.
func TestClearHistory_PlexTV_OwnerLookupFailsWithAccount(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.userStatus = http.StatusUnauthorized
	if err := r.run(t, "QuienNZ", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if r.tv.graphHits != 0 || !slices.Equal(r.pms.deleted, keys("1", "2")) {
		t.Errorf("plex.tv requests %d, server deleted %v; want none and 1, 2", r.tv.graphHits, r.pms.deleted)
	}
	if out := r.out.String(); !strings.Contains(out, "couldn't look up the PLEX_TOKEN owner") || !strings.Contains(out, "401") {
		t.Errorf("no lookup notice:\n%s", out)
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
	if len(r.tv.removed) != 0 {
		t.Errorf("plex.tv removed %v", r.tv.removed)
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
	for _, want := range []string{
		"Bluey (1992)                             1 episode(s)",
		"Bluey (2018)                             2 episode(s)",
		"Peppa Pig                                1 episode(s)",
		"plex.tv records no year for a show these matched, so it matched on title alone: Peppa Pig (2004)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Not deleting these") {
		t.Errorf("a year-less target left a year out:\n%s", out)
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
	out := r.out.String()
	if !strings.Contains(out, "Not in plex.tv history:\n  NCIS: Los Angeles") || !strings.Contains(out, "Not in this history:\n  Bandit") {
		t.Errorf("missing targets not reported per side:\n%s", out)
	}
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
	if !strings.Contains(r.out.String(), "plex.tv rate limit: waiting 1m1s") {
		t.Errorf("no wait notice:\n%s", r.out.String())
	}
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

// A delete still rate-limited after maxRateLimitTries fails, and three such
// failures in a row stop the run; the record lists every item not deleted.
func TestClearHistory_PlexTV_RateLimitGivesUp(t *testing.T) {
	r := newCloudRun(t, "y")
	r.tv.rateLimits = 1000
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "4 history entries were not deleted") {
		t.Fatalf("err = %v; want 4 not deleted", err)
	}
	if want := 3 * (maxRateLimitTries - 1); len(r.slept) != want {
		t.Errorf("slept %d times; want %d (%d tries for each of 3 items)", len(r.slept), want, maxRateLimitTries)
	}
	if want := keys("1", "2"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("server deleted %v; want %v, before plex.tv", r.pms.deleted, want)
	}
	recs := r.records(t)
	if len(recs) != 1 || !slices.Equal(recs[0].Failed, []string{"c1", "c2", "c3", "c4"}) || !recs[0].Complete {
		t.Errorf("records %+v; want one listing c1–c4 as failed", recs)
	}
	if !strings.Contains(r.out.String(), "Stopped after 3 failures in a row; 1 entries weren't tried.") {
		t.Errorf("no stop notice:\n%s", r.out.String())
	}
}

// A plex.tv error on one item is reported, and the rest go ahead.
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
	if want := "failed: plex.tv Bluey (2018) season 2 on 2026-11-20: plex.tv: removeActivity: boom"; !strings.Contains(r.out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, r.out.String())
	}
}
