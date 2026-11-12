package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tmdbStub serves /tv/999 and /tv/999/season/1 with the given status and body.
func tmdbStub(t *testing.T, showStatus int, showBody string, seasonStatus int, seasonBody string) *httpTMDBClient {
	t.Helper()
	c, srv := newTestTMDB(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/999":
			w.WriteHeader(showStatus)
			_, _ = w.Write([]byte(showBody))
		case "/tv/999/season/1":
			w.WriteHeader(seasonStatus)
			_, _ = w.Write([]byte(seasonBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return c
}

// writeEpisode creates an empty episode file under dir/Err Show.
func writeEpisode(t *testing.T, dir, name string) string {
	t.Helper()
	showDir := filepath.Join(dir, "Err Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(showDir, name)
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	stubShowOK   = `{"id":999,"name":"Err Show","first_air_date":"2022-01-01","poster_path":"/show.jpg"}`
	stubSeasonOK = `{"air_date":"2022-01-01","poster_path":"/s1.jpg","episodes":[{"episode_number":1,"air_date":"2022-01-08","overview":"Ep one"}]}`
	stubNotFound = `{"status_code":34,"status_message":"The resource you requested could not be found."}`
	stubLimited  = `{"status_code":25,"status_message":"Your request count is over the allowed limit."}`
)

func TestRun_SeasonLookupFails_EpisodeFailedNotTagged(t *testing.T) {
	dir := t.TempDir()
	ep := writeEpisode(t, dir, "Err Show - S01E01.mkv")
	tmdb := tmdbStub(t, http.StatusOK, stubShowOK, http.StatusTooManyRequests, stubLimited)
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retry := emptyRetryList(t)
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Err Show": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retry, emptyRatingCache(t), nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if stats.Failed != 1 || stats.Success != 0 {
		t.Errorf("stats = %+v; want 1 failed, 0 success", stats)
	}
	if len(runner.calls) != 0 {
		t.Errorf("mkvpropedit ran %d times; a failed season lookup must not tag the file", len(runner.calls))
	}
	if !retry.Has(ep) {
		t.Error("episode not on the retry list; it would be skipped once older than the cutoff")
	}
	if !strings.Contains(out, "[!] TMDB season lookup failed") || !strings.Contains(out, "429") {
		t.Errorf("output doesn't report the TMDB failure:\n%s", out)
	}
	if strings.Contains(out, "No air date for episode") {
		t.Errorf("a failed request must not read as missing TMDB data:\n%s", out)
	}
}

func TestRun_SeasonNotOnTMDB_StillTagsWithPlaceholder(t *testing.T) {
	dir := t.TempDir()
	ep := writeEpisode(t, dir, "Err Show - S01E01.mkv")
	tmdb := tmdbStub(t, http.StatusOK, stubShowOK, http.StatusNotFound, stubNotFound)
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retry := emptyRetryList(t)
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Err Show": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retry, emptyRatingCache(t), nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if stats.Success != 1 || stats.Failed != 0 {
		t.Errorf("stats = %+v; want 1 success", stats)
	}
	if !strings.Contains(out, "using show first air date as placeholder: 2022-01-01") {
		t.Errorf("expected the show-date placeholder:\n%s", out)
	}
	if strings.Contains(out, "lookup failed") {
		t.Errorf("a season TMDB doesn't have is not a failure:\n%s", out)
	}
	if !strings.Contains(out, "[i] TMDB has no season 1 for show ID 999") {
		t.Errorf("the missing season isn't named:\n%s", out)
	}
	if !retry.Has(ep) {
		t.Error("placeholder-dated episode should stay on the retry list")
	}
}

func TestRun_ShowLookupFails_ReportedAndEpisodeStillTagged(t *testing.T) {
	dir := t.TempDir()
	writeEpisode(t, dir, "Err Show - S01E01.mkv")
	tmdb := tmdbStub(t, http.StatusServiceUnavailable, "busy", http.StatusOK, stubSeasonOK)
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Err Show": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if !strings.Contains(out, "[!] TMDB show lookup failed for Err Show") || !strings.Contains(out, "503") {
		t.Errorf("output doesn't report the show lookup failure:\n%s", out)
	}
	if stats.Success != 1 {
		t.Errorf("stats = %+v; the season data is enough to tag the episode", stats)
	}
}

func TestRun_ExtraSeasonLookupFails_ExtraFailedNotTagged(t *testing.T) {
	dir := t.TempDir()
	extra := writeEpisode(t, dir, "Err Show - S01 - Drama CD.mkv")
	tmdb := tmdbStub(t, http.StatusOK, stubShowOK, http.StatusInternalServerError, "oops")
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retry := emptyRetryList(t)
	manual := map[string]ManualEntry{"Err Show": {Extras: map[string]ExtraEntry{
		"Err Show - S01 - Drama CD": {Episode: "S01E01", Season: 1},
	}}}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Err Show": "999"}),
		manual, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retry, emptyRatingCache(t), nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if stats.Failed != 1 || len(runner.calls) != 0 {
		t.Errorf("stats = %+v, mkvpropedit calls = %d; want the extra failed and untagged", stats, len(runner.calls))
	}
	if !retry.Has(extra) {
		t.Error("failed extra not on the retry list")
	}
	if !strings.Contains(out, "[!] TMDB season lookup failed") {
		t.Errorf("output doesn't report the TMDB failure:\n%s", out)
	}
}

func TestTitleCheck_SeasonLookupFails_Reported(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantWarn bool
	}{
		{"rate limited", http.StatusTooManyRequests, stubLimited, true},
		{"season not on TMDB", http.StatusNotFound, stubNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeEpisode(t, dir, "Err Show - S01E01 - Pilot.mkv")
			checker := NewTitleChecker(Config{MediaDir: dir}, cacheWith(t, map[string]string{"Err Show": "999"}), nil,
				tmdbStub(t, http.StatusOK, stubShowOK, tc.status, tc.body), &assertNoAskPrompter{t: t}, nil, newNullTitleCache(t), noopTimer{})

			out := captureStdout(t, checker.Run)

			if got := strings.Contains(out, "[!] TMDB season lookup failed, not checking Err Show - S01E01 - Pilot.mkv"); got != tc.wantWarn {
				t.Errorf("warning printed = %v, want %v:\n%s", got, tc.wantWarn, out)
			}
		})
	}
}

// A failed season lookup isn't cached: the next episode of the same season
// asks again and, once TMDB answers, is tagged with its real data.
func TestRun_SeasonLookupError_NotCached_NextEpisodeRetries(t *testing.T) {
	dir := t.TempDir()
	ep1 := writeEpisode(t, dir, "Err Show - S01E01.mkv")
	ep2 := writeEpisode(t, dir, "Err Show - S01E02.mkv")
	seasonHits := 0
	tmdb, srv := newTestTMDB(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/999":
			_, _ = w.Write([]byte(stubShowOK))
		case "/tv/999/season/1":
			seasonHits++
			if seasonHits == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(stubLimited))
				return
			}
			_, _ = w.Write([]byte(`{"episodes":[{"episode_number":1,"air_date":"2022-01-08"},{"episode_number":2,"air_date":"2022-01-15"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retry := emptyRetryList(t)
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Err Show": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retry, emptyRatingCache(t), nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if stats.Failed != 1 || stats.Success != 1 {
		t.Errorf("stats = %+v; want E01 failed, E02 tagged", stats)
	}
	if seasonHits != 2 {
		t.Errorf("season requested %d times; want 2 (the error must not be cached)", seasonHits)
	}
	if len(runner.calls) != 1 || !strings.HasSuffix(runner.calls[0].filePath, "S01E02.mkv") {
		t.Errorf("mkvpropedit calls = %+v; want E02 only", runner.calls)
	}
	if strings.Contains(out, "placeholder") {
		t.Errorf("E02 should get its real air date:\n%s", out)
	}
	if !retry.Has(ep1) || retry.Has(ep2) {
		t.Errorf("retry list: E01 %v (want true), E02 %v (want false)", retry.Has(ep1), retry.Has(ep2))
	}
}

// An extra whose episode reference is in a different season from its poster
// fails when the episode's season lookup fails, even though the poster's worked.
func TestRun_ExtraEpisodeRefLookupFails_ExtraFailedNotTagged(t *testing.T) {
	dir := t.TempDir()
	extra := writeEpisode(t, dir, "Err Show - S02 - Drama CD.mkv")
	tmdb, srv := newTestTMDB(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/999":
			_, _ = w.Write([]byte(stubShowOK))
		case "/tv/999/season/2":
			_, _ = w.Write([]byte(`{"poster_path":"/s2.jpg"}`))
		case "/tv/999/season/1":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retry := emptyRetryList(t)
	manual := map[string]ManualEntry{"Err Show": {Extras: map[string]ExtraEntry{
		"Err Show - S02 - Drama CD": {Episode: "S01E01", Season: 2},
	}}}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Err Show": "999"}),
		manual, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retry, emptyRatingCache(t), nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if stats.Failed != 1 || stats.Success != 0 || len(runner.calls) != 0 {
		t.Errorf("stats = %+v, mkvpropedit calls = %d; want the extra failed and untagged", stats, len(runner.calls))
	}
	if !retry.Has(extra) {
		t.Error("failed extra not on the retry list")
	}
	if !strings.Contains(out, "[!] TMDB season lookup failed: show ID 999 season 1") {
		t.Errorf("output doesn't name the failed season:\n%s", out)
	}
}
