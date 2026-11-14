package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// failingTimer is a FileTimer whose calls fail with the configured errors.
type failingTimer struct {
	timesErr, ctimeErr error
}

func (f failingTimer) SetTimes(string, time.Time) error        { return f.timesErr }
func (f failingTimer) SetCreationTime(string, time.Time) error { return f.ctimeErr }

// showLibrary makes dir/<show>/<files> and returns dir.
func showLibrary(t *testing.T, show string, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, show), 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, show, f), []byte("fake"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A file whose tags were written but whose poster or timestamps then failed
// is a failure, named on its result line, and stays on the retry list so a
// later run finishes it.
func TestRun_FailedStepsAfterTaggingCountAsFailures(t *testing.T) {
	dir := showLibrary(t, "Step Show", "Step Show - S01E01.mkv")
	file := filepath.Join(dir, "Step Show", "Step Show - S01E01.mkv")
	tmdb := &fakeTMDB{seasonResult: SeasonResult{PosterPath: "/p.jpg", Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2022-03-01"}}}}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterErr: errors.New("HTTP 503")}
	retry := emptyRetryList(t)
	retry.Add(file)
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Step Show": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{},
		failingTimer{timesErr: errors.New("denied"), ctimeErr: errors.New("denied")}, retry, emptyRatingCache(t), nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if stats.Success != 0 || stats.Failed != 1 {
		t.Errorf("stats = %+v; want the file counted as failed", stats)
	}
	if want := "[X] Processed: Step Show - S01E01.mkv (failed: poster, mtime, ctime)\n"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, "[✓]") {
		t.Errorf("a partly failed file is reported as done:\n%s", out)
	}
	if len(runner.calls) != 1 {
		t.Errorf("Apply called %d times; want 1", len(runner.calls))
	}
	if !onDiskRetry(t, retry).Has(file) {
		t.Error("partly failed file dropped from the retry list")
	}
}

func TestApplyMKV_FailedSteps(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stampDate  string
		posterErr  error
		timer      FileTimer
		wantFailed []string
		wantOut    string
	}{
		{"all done", "2022-03-01T00:00:00Z", nil, failingTimer{}, nil, ""},
		{"poster", "2022-03-01T00:00:00Z", errors.New("HTTP 503"), failingTimer{}, []string{"poster"}, "Poster download failed"},
		{"mtime", "2022-03-01T00:00:00Z", nil, failingTimer{timesErr: errors.New("denied")}, []string{"mtime"}, "Setting the modified time failed: denied"},
		{"ctime", "2022-03-01T00:00:00Z", nil, failingTimer{ctimeErr: errors.New("denied")}, []string{"ctime"}, "Setting the creation time failed: denied"},
		{"unreadable air date", "March 2022", nil, failingTimer{}, []string{"timestamps"}, `Air date "March 2022" can't be read; timestamps not set`},
		{"no air date", "", nil, failingTimer{timesErr: errors.New("unused")}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterPath: "p.jpg", posterErr: tc.posterErr}
			p := &Processor{mkv: runner, timer: tc.timer}
			var wrote bool
			var failed []string
			out := captureStdout(t, func() {
				wrote, failed = p.applyMKV("ep.mkv", "T", "", "", "", tc.stampDate, "http://example.com/p.jpg", RatingNone, false)
			})
			if !wrote {
				t.Error("wrote = false; want true, mkvpropedit succeeded")
			}
			if !slices.Equal(failed, tc.wantFailed) {
				t.Errorf("failed = %q; want %q", failed, tc.wantFailed)
			}
			if tc.wantOut != "" && !strings.Contains(out, tc.wantOut) {
				t.Errorf("output lacks %q:\n%s", tc.wantOut, out)
			}
		})
	}
}

// A failed mkvpropedit isn't a partial success: nothing is named as failed
// and no timestamps are touched.
func TestApplyMKV_NotWrittenNamesNoSteps(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyRC: 2, tagsPath: "t.xml", posterErr: errors.New("HTTP 503")}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	var wrote bool
	var failed []string
	captureStdout(t, func() {
		wrote, failed = p.applyMKV("ep.mkv", "T", "", "", "", "2022-03-01T00:00:00Z", "http://example.com/p.jpg", RatingNone, false)
	})
	if wrote || failed != nil {
		t.Errorf("wrote = %v, failed = %q; want false and none", wrote, failed)
	}
	if len(timer.setTimesCalls) != 0 {
		t.Error("timestamps set on a file mkvpropedit didn't write")
	}
}

// --ratings-only: a rating written whose timestamp restore failed is a
// failure, but is still recorded as applied, since the tags hold it.
func TestRun_RatingsOnly_TimestampRestoreFailure(t *testing.T) {
	dir := showLibrary(t, "Rated", "Rated - S01E01.mkv")
	file := filepath.Join(dir, "Rated", "Rated - S01E01.mkv")
	rc := emptyRatingCache(t)
	rc.SetShowRating("999", RatingM)
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true, RatingsOnly: true}, cacheWith(t, map[string]string{"Rated": "999"}),
		map[string]ManualEntry{}, &fakeTMDB{}, runner, &scriptedPrompter{},
		failingTimer{ctimeErr: errors.New("denied")}, emptyRetryList(t), rc, nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if stats.Success != 0 || stats.Failed != 1 {
		t.Errorf("stats = %+v; want 1 failed", stats)
	}
	if want := "[X] Rating updated: Rated - S01E01.mkv (failed: ctime)\n"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if !rc.IsApplied(file, RatingM) {
		t.Error("rating not recorded as applied although the tags were written")
	}
}

func TestRunDebandOnly_TimestampRestoreFailure(t *testing.T) {
	dir := showLibrary(t, "Banded", "Banded - S01E01.mkv", "Banded - S01E02.mkv")
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", extractTagsXML: "<Tags></Tags>"}
	manual := map[string]ManualEntry{"Banded": {DebandEpisodes: map[string]bool{"S01E01": true}}}
	p := NewProcessor(Config{MediaDir: dir}, nil, manual, nil, runner, nil,
		failingTimer{timesErr: errors.New("denied")}, nil, nil, nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.RunDebandOnly() })

	if stats.Success != 0 || stats.Failed != 1 {
		t.Errorf("stats = %+v; want 1 failed", stats)
	}
	if want := "[X] Deband updated: Banded - S01E01.mkv (failed: mtime)\n"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

func TestApplyFullTitle_TimestampRestoreFailureIsNamed(t *testing.T) {
	path := debandTestFile(t)
	runner := &fakeMKVRunner{avail: true, applyResult: "Done."}
	tc := NewTitleChecker(Config{}, nil, nil, nil, nil, runner, newNullTitleCache(t),
		failingTimer{timesErr: errors.New("denied"), ctimeErr: errors.New("denied")})

	out := captureStdout(t, func() { tc.applyFullTitle(path, "Full Title") })

	if want := "[X] MKV title set: Full Title (failed: mtime, ctime)\n"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if got, ok := tc.titleCache.Get(path); !ok || got != "Full Title" {
		t.Errorf("title cache = %q ok=%v; want it cached, the title was written", got, ok)
	}
}

// Every failed show-level Plex lookup or push is counted.
func TestApplyPlexRatings_CountsFailures(t *testing.T) {
	plexErr := errors.New("HTTP 500")
	for _, tc := range []struct {
		name string
		plex *fakePlexClient
		tmdb *fakeTMDB
		want int
	}{
		{"all fine", &fakePlexClient{showRatingKey: "k", seasons: []PlexSeason{{RatingKey: "s1", SeasonNumber: 1}},
			episodesByKey: map[string][]PlexEpisodeItem{"s1": {{RatingKey: "e1", SeasonNumber: 1, EpisodeNumber: 1}}}},
			&fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "en"}}, 0},
		{"find by TMDB ID", &fakePlexClient{findErr: plexErr}, &fakeTMDB{}, 1},
		{"seasons", &fakePlexClient{showRatingKey: "k", seasonsErr: plexErr}, &fakeTMDB{}, 1},
		// Episode, season and show ratings each fail; the language push is the fourth.
		{"rating pushes and language", &fakePlexClient{showRatingKey: "k", seasons: []PlexSeason{{RatingKey: "s1", SeasonNumber: 1}},
			episodesByKey: map[string][]PlexEpisodeItem{"s1": {{RatingKey: "e1", SeasonNumber: 1, EpisodeNumber: 1}}},
			setRatingErr:  plexErr, setLangErr: plexErr},
			&fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "en"}}, 4},
		{"TMDB language lookup", &fakePlexClient{showRatingKey: "k"}, &fakeTMDB{showErr: errors.New("TMDB down")}, 1},
		{"not in Plex", &fakePlexClient{findErr: ErrPlexNotFound, findTitleErr: ErrPlexNotFound}, &fakeTMDB{}, 0},
		{"title fallback", &fakePlexClient{findErr: ErrPlexNotFound, findTitleErr: plexErr}, &fakeTMDB{}, 1},
		{"episodes", &fakePlexClient{showRatingKey: "k", seasons: []PlexSeason{{RatingKey: "s1", SeasonNumber: 1}}, episodesErr: plexErr},
			&fakeTMDB{}, 1},
		{"metadata refresh", &fakePlexClient{showRatingKey: "k", refreshMetaErr: plexErr}, &fakeTMDB{}, 1},
		{"episode metadata", &fakePlexClient{showRatingKey: "k", seasons: []PlexSeason{{RatingKey: "s1", SeasonNumber: 1}},
			episodesByKey: map[string][]PlexEpisodeItem{"s1": {{RatingKey: "e1", SeasonNumber: 1, EpisodeNumber: 1}}},
			setEpMetaErr:  plexErr},
			&fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "en"}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := emptyRatingCache(t)
			rc.SetShowRating("111", RatingM)
			// A manual date makes the episode metadata push run.
			manual := ManualEntry{Dates: map[string]string{"S01E01": "2022-03-01"}}
			p := NewProcessor(Config{}, nil, map[string]ManualEntry{"Show A": manual}, tc.tmdb, nil, nil, nil, nil, rc, nil, tc.plex, nil)
			var errs int
			captureStdout(t, func() { errs = p.applyPlexRatings("111", nil, "Show A", "Show A", true, manual) })
			if errs != tc.want {
				t.Errorf("errs = %d; want %d", errs, tc.want)
			}
		})
	}
}

// The full run's summary carries the show-level Plex failures.
func TestRun_PlexFailuresReachStats(t *testing.T) {
	dir := showLibrary(t, "Plexy", "Plexy - S01E01.mkv")
	rc := emptyRatingCache(t)
	rc.SetShowRating("999", RatingM)
	tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2022-03-01"}}}}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	plex := &fakePlexClient{showRatingKey: "k", seasonsErr: errors.New("HTTP 500")}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Plexy": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, emptyRetryList(t), rc, nil, plex, nil)

	var stats Stats
	captureStdout(t, func() { stats = p.Run() })

	if stats.Success != 1 || stats.PlexErrors != 1 {
		t.Errorf("stats = %+v; want the file a success and 1 Plex error", stats)
	}
}

// A fully tagged file leaves the retry list; one whose poster failed stays.
func TestRun_RetryListFollowsFailedSteps(t *testing.T) {
	for _, tc := range []struct {
		name      string
		posterErr error
		wantRetry bool
	}{
		{"all done", nil, false},
		{"poster failed", errors.New("HTTP 503"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := showLibrary(t, "Step Show", "Step Show - S01E01.mkv")
			file := filepath.Join(dir, "Step Show", "Step Show - S01E01.mkv")
			tmdb := &fakeTMDB{seasonResult: SeasonResult{PosterPath: "/p.jpg", Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2022-03-01"}}}}
			runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterPath: "p.jpg", posterErr: tc.posterErr}
			retry := emptyRetryList(t)
			retry.Add(file)
			p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Step Show": "999"}),
				map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retry, emptyRatingCache(t), nil, nil, nil)

			captureStdout(t, func() { p.Run() })

			if got := onDiskRetry(t, retry).Has(file); got != tc.wantRetry {
				t.Errorf("on retry list = %v; want %v", got, tc.wantRetry)
			}
		})
	}
}

// A manual extra whose timestamps failed after tagging is a failure, named on
// its result line, and stays on the retry list.
func TestRun_ExtraFailedStepsCountAsFailures(t *testing.T) {
	dir := showLibrary(t, "My Show", "My Show - S02 - Drama CD.mkv")
	file := filepath.Join(dir, "My Show", "My Show - S02 - Drama CD.mkv")
	manual := map[string]ManualEntry{"My Show": {Extras: map[string]ExtraEntry{
		"My Show - S02 - Drama CD": {Date: "2022-04-01", Season: 2},
	}}}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retry := emptyRetryList(t)
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"My Show": "999"}),
		manual, &fakeTMDB{}, runner, &scriptedPrompter{},
		failingTimer{ctimeErr: errors.New("denied")}, retry, emptyRatingCache(t), nil, nil, nil)

	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })

	if stats.Success != 0 || stats.Failed != 1 {
		t.Errorf("stats = %+v; want the extra counted as failed", stats)
	}
	if want := "[X] Processed (extra): My Show - S02 - Drama CD.mkv (failed: ctime)\n"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if !onDiskRetry(t, retry).Has(file) {
		t.Error("partly failed extra dropped from the retry list")
	}
}

// The library re-scan after tagging extras counts as a Plex error only when it fails.
func TestRun_PostRunLibraryRefresh(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		wantErr int
		wantOut string
	}{
		{"fails", errors.New("HTTP 500"), 1, "Plex library refresh failed: HTTP 500"},
		{"succeeds", nil, 0, "Plex: library re-scanned for extras."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := showLibrary(t, "My Show", "My Show - S02 - Drama CD.mkv")
			manual := map[string]ManualEntry{"My Show": {Extras: map[string]ExtraEntry{
				"My Show - S02 - Drama CD": {Date: "2022-04-01", Season: 2},
			}}}
			runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
			plex := &fakePlexClient{showRatingKey: "k", refreshLibsErr: tc.err}
			p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"My Show": "999"}),
				manual, &fakeTMDB{}, runner, &scriptedPrompter{}, &captureFileTimer{}, emptyRetryList(t), emptyRatingCache(t), nil, plex, nil)

			var stats Stats
			out := captureStdout(t, func() { stats = p.Run() })

			if stats.Success != 1 || stats.PlexErrors != tc.wantErr {
				t.Errorf("stats = %+v; want the extra a success and %d Plex errors", stats, tc.wantErr)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output lacks %q:\n%s", tc.wantOut, out)
			}
		})
	}
}

// onDiskRetry reloads r from the file Run saved it to.
func onDiskRetry(t *testing.T, r *RetryList) *RetryList {
	t.Helper()
	got, err := LoadRetryList(r.path)
	if err != nil {
		t.Fatalf("reload %s: %v", r.path, err)
	}
	return got
}

// stepShowRun runs one pass over "Step Show - S01E01.mkv" in dir (dated
// 2020, before cutoff), with the retry list loaded from retryPath.
func stepShowRun(t *testing.T, dir, retryPath string, cutoff time.Time, airDate string, runner *fakeMKVRunner) (Stats, string) {
	t.Helper()
	retry, err := LoadRetryList(retryPath)
	if err != nil {
		t.Fatal(err)
	}
	tmdb := &fakeTMDB{seasonResult: SeasonResult{PosterPath: "/p.jpg", Episodes: []Episode{{EpisodeNumber: 1, AirDate: airDate}}}}
	p := NewProcessor(Config{MediaDir: dir, CutoffDate: cutoff, ProcessAll: true}, cacheWith(t, map[string]string{"Step Show": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retry, emptyRatingCache(t), nil, nil, nil)
	var stats Stats
	out := captureStdout(t, func() { stats = p.Run() })
	return stats, out
}

// oldStepShow makes the Step Show library with its episode dated before any cutoff.
func oldStepShow(t *testing.T) (dir, file, retryPath string) {
	t.Helper()
	dir = showLibrary(t, "Step Show", "Step Show - S01E01.mkv")
	file = filepath.Join(dir, "Step Show", "Step Show - S01E01.mkv")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	return dir, file, filepath.Join(t.TempDir(), "pending_dates.json")
}

// A file whose poster failed is picked up by the next run although it is
// older than the cutoff, and leaves the list once it succeeds.
func TestRun_RetryListSecondRunFinishesFile(t *testing.T) {
	dir, file, retryPath := oldStepShow(t)

	// Run 1, as with --all: the poster fails.
	stepShowRun(t, dir, retryPath, time.Time{}, "2020-01-01",
		&fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterErr: errors.New("HTTP 503")})
	if r, _ := LoadRetryList(retryPath); !r.Has(file) || r.Attempts(file) != 1 {
		t.Fatalf("after run 1: on list = %v, attempts = %d; want true, 1", r.Has(file), r.Attempts(file))
	}

	// Run 2, with today's cutoff: the poster works.
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterPath: "p.jpg"}
	stats, _ := stepShowRun(t, dir, retryPath, time.Now(), "2020-01-01", runner)
	if len(runner.calls) != 1 || stats.Success != 1 {
		t.Errorf("run 2: Apply calls = %d, stats = %+v; want the old file processed", len(runner.calls), stats)
	}
	if r, _ := LoadRetryList(retryPath); r.Has(file) {
		t.Error("finished file still on the retry list")
	}
}

// A file that fails a step on maxRetryAttempts runs in a row is dropped, and
// a run with the cutoff then skips it.
func TestRun_RetryListGivesUp(t *testing.T) {
	dir, file, retryPath := oldStepShow(t)
	failing := func() *fakeMKVRunner {
		return &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterErr: errors.New("HTTP 404")}
	}

	stepShowRun(t, dir, retryPath, time.Time{}, "2020-01-01", failing()) // as with --all
	for run := 2; run <= maxRetryAttempts; run++ {
		_, out := stepShowRun(t, dir, retryPath, time.Now(), "2020-01-01", failing())
		r, _ := LoadRetryList(retryPath)
		if run < maxRetryAttempts {
			if r.Attempts(file) != run {
				t.Fatalf("after run %d: attempts = %d (on list %v)", run, r.Attempts(file), r.Has(file))
			}
			continue
		}
		if r.Has(file) {
			t.Fatalf("still on the retry list after %d failed runs", run)
		}
		want := "Not retrying Step Show - S01E01.mkv after 5 runs in a row failed (failed: poster); run with --all to try again."
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	runner := failing()
	stats, _ := stepShowRun(t, dir, retryPath, time.Now(), "2020-01-01", runner)
	if len(runner.calls) != 0 || stats.SkippedOld != 1 {
		t.Errorf("after giving up: Apply calls = %d, stats = %+v; want the file skipped as old", len(runner.calls), stats)
	}
}

// Waiting for TMDB to list an air date isn't a failure, so it never uses up
// the retry attempts.
func TestRun_RetryListPlaceholderDateNotCapped(t *testing.T) {
	dir, file, retryPath := oldStepShow(t)
	ok := func() *fakeMKVRunner {
		return &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterPath: "p.jpg"}
	}
	stepShowRun(t, dir, retryPath, time.Time{}, "", ok())
	for run := 2; run <= maxRetryAttempts+1; run++ {
		stepShowRun(t, dir, retryPath, time.Now(), "", ok())
	}
	if r, _ := LoadRetryList(retryPath); !r.Has(file) || r.Attempts(file) != 0 {
		t.Errorf("on list = %v, attempts = %d; want true, 0", r.Has(file), r.Attempts(file))
	}
}

// A file whose tags couldn't be written at all goes on the retry list.
func TestRun_TagWriteFailureRetried(t *testing.T) {
	dir, file, retryPath := oldStepShow(t)
	stats, _ := stepShowRun(t, dir, retryPath, time.Time{}, "2020-01-01",
		&fakeMKVRunner{avail: true, applyRC: 2, tagsPath: "t.xml"})

	if stats.Failed != 1 {
		t.Errorf("stats = %+v; want 1 failed", stats)
	}
	if r, _ := LoadRetryList(retryPath); !r.Has(file) || r.Attempts(file) != 1 {
		t.Errorf("on list = %v, attempts = %d; want true, 1", r.Has(file), r.Attempts(file))
	}
}

// The retry list reads the older array form and keeps each entry's count.
func TestRetryList_Formats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending_dates.json")
	if err := os.WriteFile(path, []byte(`["C:\\tv\\a.mkv", "C:\\tv\\b.mkv"]`), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRetryList(path)
	if err != nil {
		t.Fatalf("old format: %v", err)
	}
	a, b := `C:\tv\a.mkv`, `C:\tv\b.mkv`
	if !r.Has(a) || !r.Has(b) || r.Attempts(a) != 0 {
		t.Fatalf("old format loaded %v", r.data)
	}

	r.Fail(a)
	r.Add(a) // a TMDB lookup failure keeps the count
	r.Fail(b)
	r.Keep(b) // a run with no failed step clears it
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRetryList(path)
	if err != nil {
		t.Fatalf("new format: %v", err)
	}
	if got.Attempts(a) != 1 || !got.Has(b) || got.Attempts(b) != 0 {
		t.Errorf("reloaded %v; want a:1 b:0", got.data)
	}
}
