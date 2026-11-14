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
// is a failure, named on its result line, and still leaves the retry list.
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
	if retry.Has(file) {
		t.Error("file left on the retry list although its tags and date were written")
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := emptyRatingCache(t)
			rc.SetShowRating("111", RatingM)
			p := NewProcessor(Config{}, nil, nil, tc.tmdb, nil, nil, nil, nil, rc, nil, tc.plex, nil)
			var errs int
			captureStdout(t, func() { errs = p.applyPlexRatings("111", nil, "Show A", "Show A", false, ManualEntry{}) })
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
