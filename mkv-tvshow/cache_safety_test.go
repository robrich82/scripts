package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// captureOutput runs fn and returns what it wrote to stdout and stderr,
// interleaved as written.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	fn()
	w.Close()
	return string(<-done)
}

// runShows sets up one episode per show under a fresh media dir and runs a
// full pass over them, with every show in the show cache and TMDB rating PG.
// It returns the run's output (stdout and stderr) and stats.
func runShows(t *testing.T, cache *ShowCache, rc *RatingCache, plex PlexClient, prompter Prompter, shows ...string) (string, Stats) {
	t.Helper()
	return runShowsRetry(t, emptyRetryList(t), cache, rc, plex, prompter, shows...)
}

// runShowsRetry is runShows with the given retry list.
func runShowsRetry(t *testing.T, retry *RetryList, cache *ShowCache, rc *RatingCache, plex PlexClient, prompter Prompter, shows ...string) (string, Stats) {
	t.Helper()
	dir := t.TempDir()
	for _, show := range shows {
		showDir := filepath.Join(dir, show)
		if err := os.MkdirAll(showDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(showDir, show+" - S01E01.mkv"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tmdb := &fakeTMDB{
		searchResults: []ShowResult{{ID: 999, Name: "Found"}},
		showDetails:   ShowDetails{FirstAirDate: "2022-01-01"},
		seasonResult:  SeasonResult{AirDate: "2022-02-01", Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2022-03-01"}}},
		contentRating: RatingPG,
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, prompter,
		&captureFileTimer{}, retry, rc, []RatingSource{tmdb}, plex, nil)
	var stats Stats
	out := captureOutput(t, func() { stats = p.Run() })
	return out, stats
}

// The rating cache is saved after Plex is updated, so the last show's Plex key
// reaches disk rather than only the next show's save.
func TestProcessorRun_SavesPlexStateOfLastShow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rating_cache.json")
	rc, err := LoadRatingCache(path)
	if err != nil {
		t.Fatal(err)
	}
	cache := cacheWith(t, map[string]string{"Show A": "999"})

	_, _ = runShows(t, cache, rc, &fakePlexClient{showRatingKey: "k1"}, &scriptedPrompter{}, "Show A")

	onDisk, err := LoadRatingCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if key, _ := onDisk.GetPlexKey("999"); key != "k1" {
		t.Errorf("Plex key on disk = %q; want k1", key)
	}
	if r, _ := onDisk.GetShowRating("999"); r != RatingPG {
		t.Errorf("show rating on disk = %q; want PG", r)
	}
}

// A rating cache save that keeps failing is printed once, and the run ends by
// saying what was lost.
func TestProcessorRun_RatingSaveFailureReportedOnce(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	rc, err := LoadRatingCache(filepath.Join(t.TempDir(), "missing", "rating_cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	cache := cacheWith(t, map[string]string{"Show A": "999", "Show B": "999"})

	out, stats := runShows(t, cache, rc, nil, &scriptedPrompter{}, "Show A", "Show B")

	if n := strings.Count(out, "[!] Failed to save rating cache"); n != 1 {
		t.Errorf("save failure printed %d times; want once:\n%s", n, out)
	}
	if !strings.Contains(out, "[!] The rating cache "+rc.path+" was never saved; this run's ratings and Plex state are lost") {
		t.Errorf("no end-of-run warning:\n%s", out)
	}
	if stats.LostSaves != 1 {
		t.Errorf("LostSaves = %d; want 1", stats.LostSaves)
	}
}

// A processor without the caches a mode doesn't use finishes its run.
func TestProcessorRun_NilCaches(t *testing.T) {
	p := NewProcessor(Config{MediaDir: t.TempDir(), ProcessAll: true}, nil, map[string]ManualEntry{}, &fakeTMDB{},
		&fakeMKVRunner{avail: true}, &scriptedPrompter{}, &captureFileTimer{}, nil, nil, nil, nil, nil)
	var stats Stats
	captureOutput(t, func() { stats = p.Run() })
	if stats.LostSaves != 0 {
		t.Errorf("LostSaves = %d; want 0", stats.LostSaves)
	}
}

// A show match whose save fails is retried after the run; the run says whether
// the retry saved it.
func TestProcessorRun_ShowCacheSaveRetriedAtEnd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recovers bool
		want     string
	}{
		{"recovers", true, "[i] Show cache saved."},
		{"fails again", false, "show_cache.json was never saved; this run's show matches are lost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "missing")
			cache, err := LoadCache(filepath.Join(cfg, "show_cache.json"))
			if err != nil {
				t.Fatal(err)
			}
			setBeforeFinalSave(t, func() {
				if tc.recovers {
					if err := os.Mkdir(cfg, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			})

			out, _ := runShows(t, cache, emptyRatingCache(t), nil, &scriptedPrompter{answers: []string{"y"}}, "New Show")

			if !strings.Contains(out, "[!] Failed to save show cache") || !strings.Contains(out, tc.want) {
				t.Errorf("want the save failure and %q:\n%s", tc.want, out)
			}
			if !tc.recovers {
				return
			}
			onDisk, err := LoadCache(filepath.Join(cfg, "show_cache.json"))
			if err != nil {
				t.Fatal(err)
			}
			if len(onDisk.AllEntries()) != 1 {
				t.Errorf("show cache on disk = %v; want New Show's match", onDisk.AllEntries())
			}
		})
	}
}

// The retry list is saved once, after the run, with one retry; a save that
// fails twice says what was lost.
func TestProcessorRun_RetryListSaveRetried(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recovers bool
	}{{"recovers", true}, {"fails again", false}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "missing")
			retry := &RetryList{path: filepath.Join(cfg, "pending_dates.json"), data: map[string]int{}}
			setBeforeFinalSave(t, func() {
				if tc.recovers {
					if err := os.Mkdir(cfg, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			})

			out, stats := runShowsRetry(t, retry, cacheWith(t, map[string]string{"Show A": "999"}), emptyRatingCache(t), nil,
				&scriptedPrompter{}, "Show A")

			lost := strings.Contains(out, "[!] Failed to save retry list "+retry.path+"; this run's changes to the episodes to retry are lost")
			if lost == tc.recovers {
				t.Errorf("loss reported = %v; want %v:\n%s", lost, !tc.recovers, out)
			}
			if (stats.LostSaves == 1) != !tc.recovers {
				t.Errorf("LostSaves = %d; want lost = %v", stats.LostSaves, !tc.recovers)
			}
			if _, err := os.Stat(retry.path); (err == nil) != tc.recovers {
				t.Errorf("retry list on disk = %v; want it saved = %v", err, tc.recovers)
			}
		})
	}
}

// --title-check retries a failed title cache save at the end, and says what
// was lost if that fails too.
func TestTitleCheck_TitleCacheSaveFailure(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Show", "Show - S01E02 - Ami Has Arrived.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	titles, err := LoadTitleCache(filepath.Join(t.TempDir(), "missing", "title_cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 2, Name: "Episode 2"}}}}
	checker := NewTitleChecker(Config{MediaDir: dir}, cacheWith(t, map[string]string{"Show": "999"}), nil, tmdb,
		&assertNoAskPrompter{t: t}, nil, titles, noopTimer{})

	out := captureOutput(t, checker.Run)

	for _, want := range []string{
		"[!] Failed to save title cache " + titles.path,
		"[!] The title cache " + titles.path + " was never saved; this run's full titles are lost",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if checker.LostSaves != 1 {
		t.Errorf("LostSaves = %d; want 1", checker.LostSaves)
	}
}

// --title-check runs without a title cache, which it documents as allowed.
func TestTitleCheck_NilTitleCache(t *testing.T) {
	checker := NewTitleChecker(Config{MediaDir: t.TempDir()}, cacheWith(t, map[string]string{}), nil, &fakeTMDB{},
		&assertNoAskPrompter{t: t}, nil, nil, noopTimer{})
	captureOutput(t, checker.Run)
	if checker.LostSaves != 0 {
		t.Errorf("LostSaves = %d; want 0", checker.LostSaves)
	}
}

// --missing retries a failed ignore list save at the end, and says what was
// lost if that fails too.
func TestMissing_IgnoreListSaveFailure(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Show", "Show - S01E01.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ignores, err := LoadMissingIgnoreList(filepath.Join(t.TempDir(), "missing", "missing_ignore.json"))
	if err != nil {
		t.Fatal(err)
	}
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{Name: "Show", Seasons: []ShowSeason{{SeasonNumber: 1}}},
		seasonResult: SeasonResult{Episodes: []Episode{
			{EpisodeNumber: 1, AirDate: "2020-01-01"},
			{EpisodeNumber: 2, AirDate: "2020-01-08"},
		}},
	}
	checker := NewMissingChecker(Config{MediaDir: dir}, cacheWith(t, map[string]string{"Show": "999"}), nil, tmdb,
		&scriptedPrompter{answers: []string{"all"}}, ignores)

	out := captureOutput(t, func() {
		if err := checker.Run(); err != nil {
			t.Error(err)
		}
	})

	for _, want := range []string{
		"[!] Failed to save ignore list " + ignores.path,
		"[!] The ignore list " + ignores.path + " was never saved; this run's ignore choices are lost",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if checker.LostSaves != 1 {
		t.Errorf("LostSaves = %d; want 1", checker.LostSaves)
	}
}

// staleCaches fills every --clean cache under cfgHome with one entry for a
// show folder that no longer exists, and returns the config dir.
func staleCaches(t *testing.T, cfgHome string) string {
	t.Helper()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "Gone Show")
	goneFile := filepath.Join(gone, "S01E01.mkv")

	shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
	shows.Set(gone, "200")
	mustSave(t, shows.Save())
	titles, _ := LoadTitleCache(filepath.Join(cfg, "title_cache.json"))
	titles.Set(goneFile, "title")
	mustSave(t, titles.Save())
	ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	ratings.SetShowRating("200", RatingPG)
	mustSave(t, ratings.Save())
	retries, _ := LoadRetryList(filepath.Join(cfg, "pending_dates.json"))
	retries.Add(goneFile)
	mustSave(t, retries.Save())
	ignores, _ := LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
	ignores.AddShow("200")
	mustSave(t, ignores.Save())
	return cfg
}

// readOnly makes file read-only, so renaming a new copy over it fails (on
// Windows; elsewhere the rename succeeds, so callers skip).
func readOnly(t *testing.T, file string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("a read-only file blocks a rename over it only on Windows")
	}
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })
}

// unwritable makes saving file fail while it still loads: on Windows by
// making it read-only, elsewhere by making its folder read-only, which also
// blocks saving the folder's other files.
func unwritable(t *testing.T, file string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		readOnly(t, file)
		return
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only folder")
	}
	dir := filepath.Dir(file)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// cleanRemoved reports whether out says --clean removed entries from file.
func cleanRemoved(out, file string) bool {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(file) + `:\s+removed`).MatchString(out)
}

// A cache --clean can't save is reported with its path and a hint, left out of
// the total, and makes --clean exit 1. When it is one pruned by show ID,
// show_cache.json is left unchanged, so rerunning --clean once the cache can
// be saved finishes the job; the file-path caches don't need it held back.
func TestMain_Clean_DependentCacheSaveFails(t *testing.T) {
	for _, tc := range []struct {
		file     string
		holdBack bool // show_cache.json is left unchanged
	}{
		{"title_cache.json", false},
		{"rating_cache.json", true},
		{"pending_dates.json", false},
		{"missing_ignore.json", true},
	} {
		file := tc.file
		t.Run(file, func(t *testing.T) {
			cfgHome := t.TempDir()
			cfg := staleCaches(t, cfgHome)
			readOnly(t, filepath.Join(cfg, file))

			out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

			if err == nil {
				t.Errorf("--clean succeeded:\n%s", out)
			}
			total := "Total: 4 stale entries removed." // the three other dependent caches and show_cache.json
			if tc.holdBack {
				total = "Total: 3 stale entries removed."
			}
			for _, want := range []string{
				"Error saving " + filepath.Join(cfg, file) + ": ",
				"Access was denied: the file is read-only",
				"Nothing was removed from " + file + ".",
				total,
			} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if got := strings.Contains(out, "show_cache.json was left unchanged"); got != tc.holdBack {
				t.Errorf("show_cache.json left unchanged = %v; want %v:\n%s", got, tc.holdBack, out)
			}
			if cleanRemoved(out, file) || cleanRemoved(out, "show_cache.json") == tc.holdBack {
				t.Errorf("removals reported wrongly:\n%s", out)
			}
			if strings.Count(out, filepath.Join(cfg, file)) != 1 {
				t.Errorf("the cache path is repeated:\n%s", out)
			}

			// Once the cache can be saved, a rerun removes what the first run couldn't.
			if err := os.Chmod(filepath.Join(cfg, file), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err = runMain(t, t.TempDir(), cfgHome, "--clean")
			if err != nil || !cleanRemoved(out, file) || cleanRemoved(out, "show_cache.json") != tc.holdBack {
				t.Errorf("rerun = %v; want %s cleaned, and show_cache.json if it was held back:\n%s", err, file, out)
			}
			ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
			ignores, _ := LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
			if _, ok := ratings.GetShowRating("200"); ok || ignores.IsShowIgnored("200") {
				t.Errorf("show 200 kept after the rerun: rating %v, ignored %v", ok, ignores.IsShowIgnored("200"))
			}
		})
	}
}

// A failed save with nothing else removed reports neither a clean cache nor a
// total.
func TestMain_Clean_OnlyRemovalFailsToSave(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	titles, _ := LoadTitleCache(filepath.Join(cfg, "title_cache.json"))
	titles.Set(filepath.Join(t.TempDir(), "gone.mkv"), "title")
	mustSave(t, titles.Save())
	unwritable(t, filepath.Join(cfg, "title_cache.json"))

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

	if err == nil || !strings.Contains(out, "Error saving ") {
		t.Errorf("--clean = %v; want the save error:\n%s", err, out)
	}
	for _, unwanted := range []string{"All caches are clean", "Total:"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output has %q:\n%s", unwanted, out)
		}
	}
}

// With no show cache to compare against, --clean keeps the show IDs in the
// rating cache and ignore list, with or without --all-ids.
func TestMain_Clean_EmptyShowCacheKeepsShowIDs(t *testing.T) {
	for _, tc := range []struct {
		name, showCache string // "" leaves show_cache.json missing
		args            []string
		note            bool // --all-ids says why it kept them
	}{
		{"missing", "", []string{"--clean"}, false},
		{"empty", "{}", []string{"--clean"}, false},
		{"missing, --all-ids", "", []string{"--clean", "--all-ids"}, true},
		{"empty, --all-ids", "{}", []string{"--clean", "--all-ids"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			cfg := filepath.Join(cfgHome, "mkv-tvshow")
			if err := os.MkdirAll(cfg, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.showCache != "" {
				if err := os.WriteFile(filepath.Join(cfg, "show_cache.json"), []byte(tc.showCache), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
			ratings.SetShowRating("200", RatingPG)
			mustSave(t, ratings.Save())
			ignores, _ := LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
			ignores.AddShow("200")
			mustSave(t, ignores.Save())

			out, err := runMain(t, t.TempDir(), cfgHome, tc.args...)

			if err != nil {
				t.Errorf("--clean = %v:\n%s", err, out)
			}
			if got := strings.Contains(out, "--all-ids keeps the show IDs"); got != tc.note {
				t.Errorf("kept-IDs note = %v; want %v:\n%s", got, tc.note, out)
			}
			ratings, _ = LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
			if _, ok := ratings.GetShowRating("200"); !ok {
				t.Error("rating_cache.json lost show 200")
			}
			ignores, _ = LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
			if !ignores.IsShowIgnored("200") {
				t.Error("missing_ignore.json lost show 200")
			}
		})
	}
}

// A show missing from show_cache.json whose folder --clean didn't remove (a
// rebuilt cache, a manual_data override's ID) keeps its rating and ignores;
// --all-ids removes them. A gone show's ID that another folder still uses is
// kept either way.
func TestMain_Clean_OnlyGoneShowsLoseRatings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		wantOther bool // show 300, in no show folder, is kept
	}{
		{"default", []string{"--clean"}, true},
		{"--all-ids", []string{"--clean", "--all-ids"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			cfg := filepath.Join(cfgHome, "mkv-tvshow")
			if err := os.MkdirAll(cfg, 0o755); err != nil {
				t.Fatal(err)
			}
			media := t.TempDir()
			for _, name := range []string{"Show A", "Show A Again"} {
				if err := os.Mkdir(filepath.Join(media, name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
			shows.Set(filepath.Join(media, "Show A"), "100")
			shows.Set(filepath.Join(media, "Show A Again"), "100")
			shows.Set(filepath.Join(media, "Gone"), "200")
			shows.Set(filepath.Join(media, "Gone Copy"), "100") // gone, but 100 is still in use
			mustSave(t, shows.Save())
			ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
			ignores, _ := LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
			for _, id := range []string{"100", "200", "300"} {
				ratings.SetShowRating(id, RatingPG)
				ignores.AddShow(id)
			}
			mustSave(t, ratings.Save())
			mustSave(t, ignores.Save())

			out, err := runMain(t, t.TempDir(), cfgHome, tc.args...)

			if err != nil {
				t.Fatalf("--clean = %v:\n%s", err, out)
			}
			ratings, _ = LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
			ignores, _ = LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
			for id, want := range map[string]bool{"100": true, "200": false, "300": tc.wantOther} {
				if _, ok := ratings.GetShowRating(id); ok != want {
					t.Errorf("rating for show %s kept = %v; want %v", id, ok, want)
				}
				if got := ignores.IsShowIgnored(id); got != want {
					t.Errorf("ignore for show %s kept = %v; want %v", id, got, want)
				}
			}
			shows, _ = LoadCache(filepath.Join(cfg, "show_cache.json"))
			if n := len(shows.AllEntries()); n != 2 {
				t.Errorf("show_cache.json has %d entries; want the 2 existing folders", n)
			}
		})
	}
}

// With every show folder gone, --all-ids removes those shows' ratings and
// ignores, but not every other show ID: with nothing left to compare against,
// all of them would look stale.
func TestMain_Clean_AllFoldersGoneWithAllIDs(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := staleCaches(t, cfgHome)
	ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	ratings.SetShowRating("300", RatingM) // never in show_cache.json
	mustSave(t, ratings.Save())

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean", "--all-ids")

	if err != nil {
		t.Fatalf("--clean --all-ids = %v:\n%s", err, out)
	}
	if !strings.Contains(out, "Every show folder in show_cache.json is gone, so --all-ids removes only those shows' IDs") {
		t.Errorf("output lacks the gone-folders note:\n%s", out)
	}
	ratings, _ = LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	ignores, _ := LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
	if _, ok := ratings.GetShowRating("200"); ok || ignores.IsShowIgnored("200") {
		t.Errorf("gone show 200 kept: rating %v, ignored %v", ok, ignores.IsShowIgnored("200"))
	}
	if _, ok := ratings.GetShowRating("300"); !ok {
		t.Error("show 300, never in show_cache.json, lost its rating")
	}
}

// A rating_cache.json save that fails when only file paths were pruned
// doesn't hold back show_cache.json: no show ID removal needs redoing.
func TestMain_Clean_RatingPathsOnlyFailureSavesShowCache(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	media := t.TempDir()
	if err := os.Mkdir(filepath.Join(media, "Show A"), 0o755); err != nil {
		t.Fatal(err)
	}
	shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
	shows.Set(filepath.Join(media, "Show A"), "100")
	shows.Set(filepath.Join(media, "Gone Copy"), "100") // gone, but 100 is still in use
	mustSave(t, shows.Save())
	ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	ratings.SetShowRating("100", RatingPG)
	ratings.MarkApplied(filepath.Join(media, "Gone Copy", "S01E01.mkv"), RatingPG)
	mustSave(t, ratings.Save())
	readOnly(t, filepath.Join(cfg, "rating_cache.json"))

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

	if err == nil || !strings.Contains(out, "Nothing was removed from rating_cache.json.") {
		t.Errorf("--clean = %v; want the rating cache save error:\n%s", err, out)
	}
	if strings.Contains(out, "show_cache.json was left unchanged") || !cleanRemoved(out, "show_cache.json") {
		t.Errorf("show_cache.json held back although no show ID was removed:\n%s", out)
	}
}

// --all-ids only means something to --clean.
func TestMain_AllIDsNeedsClean(t *testing.T) {
	out, err := runMain(t, t.TempDir(), t.TempDir(), "--all-ids")
	if err == nil || !strings.Contains(out, "--all-ids only works with --clean") {
		t.Errorf("--all-ids alone = %v:\n%s", err, out)
	}
}

// show_cache.json is saved after the caches pruned against it; when its save
// fails, a rerun finds the same gone folders and finishes.
func TestMain_Clean_ShowCacheSaveFails(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := staleCaches(t, cfgHome)
	readOnly(t, filepath.Join(cfg, "show_cache.json"))

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

	if err == nil {
		t.Errorf("--clean succeeded:\n%s", out)
	}
	if !strings.Contains(out, "Error saving "+filepath.Join(cfg, "show_cache.json")+": ") ||
		!strings.Contains(out, "Nothing was removed from show_cache.json.") {
		t.Errorf("no save error:\n%s", out)
	}
	if cleanRemoved(out, "show_cache.json") {
		t.Errorf("an unsaved removal was reported:\n%s", out)
	}

	if err := os.Chmod(filepath.Join(cfg, "show_cache.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runMain(t, t.TempDir(), cfgHome, "--clean")
	if err != nil || !cleanRemoved(out, "show_cache.json") {
		t.Errorf("rerun = %v; want show_cache.json cleaned:\n%s", err, out)
	}
}

// --clean saves only the caches it removed something from, so a clean cache it
// can't write is no error.
func TestMain_Clean_NothingStaleWritesNothing(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
	mustSave(t, shows.Save())
	unwritable(t, filepath.Join(cfg, "show_cache.json"))

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

	if err != nil || !strings.Contains(out, "All caches are clean; nothing to remove.") {
		t.Errorf("--clean = %v:\n%s", err, out)
	}
}

// --reset-rating and --set-rating that can't save the rating cache say so,
// change nothing, and exit 1.
func TestMain_RatingModes_SaveFails(t *testing.T) {
	for _, tc := range []struct {
		name, stdin string
		args        []string
		want        string
	}{
		{"reset", "", []string{"--reset-rating", "Show"}, "Nothing was reset."},
		{"set", "y\n", []string{"--set-rating", "R16"}, "No rating was set."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			media := t.TempDir()
			showDir := filepath.Join(media, "Show")
			if err := os.Mkdir(showDir, 0o755); err != nil {
				t.Fatal(err)
			}
			episode := filepath.Join(showDir, "Show - S01E01.mkv")
			if err := os.WriteFile(episode, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := filepath.Join(cfgHome, "mkv-tvshow")
			if err := os.MkdirAll(cfg, 0o755); err != nil {
				t.Fatal(err)
			}
			shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
			shows.Set(showDir, "200")
			mustSave(t, shows.Save())
			ratingPath := filepath.Join(cfg, "rating_cache.json")
			ratings, _ := LoadRatingCache(ratingPath)
			ratings.SetShowRating("200", RatingPG)
			ratings.MarkApplied(episode, RatingPG)
			mustSave(t, ratings.Save())
			unwritable(t, ratingPath)

			out, err := runMainStdin(t, media, cfgHome, nil, tc.stdin, tc.args...)

			if err == nil {
				t.Errorf("succeeded:\n%s", out)
			}
			if !strings.Contains(out, "Error saving "+ratingPath+": ") || !strings.Contains(out, tc.want) {
				t.Errorf("want the save error and %q:\n%s", tc.want, out)
			}
			for _, unwanted := range []string{"Reset: ", "Done."} {
				if strings.Contains(out, unwanted) {
					t.Errorf("output has %q:\n%s", unwanted, out)
				}
			}
			ratings, _ = LoadRatingCache(ratingPath)
			if r, _ := ratings.GetShowRating("200"); r != RatingPG || !ratings.IsApplied(episode, RatingPG) {
				t.Errorf("rating cache changed: show rating %q, applied %v", r, ratings.IsApplied(episode, RatingPG))
			}
		})
	}
}

// A normal run whose cache save is lost ends with the count on stderr and
// exits 1.
func TestMain_LostSaveExits(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	retries, _ := LoadRetryList(filepath.Join(cfg, "pending_dates.json"))
	mustSave(t, retries.Save())
	unwritable(t, filepath.Join(cfg, "pending_dates.json"))

	out, err := runMainEnv(t, t.TempDir(), cfgHome, []string{"TMDB_API_KEY=unused"})

	if err == nil {
		t.Errorf("succeeded:\n%s", out)
	}
	for _, want := range []string{"[!] Failed to save retry list", "Done. Success: 0", "Cache saves failed: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestLostSavesLine(t *testing.T) {
	if got := lostSavesLine(0); got != "" {
		t.Errorf("lostSavesLine(0) = %q; want none", got)
	}
	if got := lostSavesLine(2); !strings.HasPrefix(got, "Cache saves failed: 2") {
		t.Errorf("lostSavesLine(2) = %q", got)
	}
}

// A save error's message names the cache once, so the paths the OS error
// carries are dropped.
func TestWithoutPaths(t *testing.T) {
	inner := fs.ErrPermission
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"rename", &os.LinkError{Op: "rename", Old: "c/.tmp-1", New: "c/cache.json", Err: inner}, inner},
		{"create temp", &fs.PathError{Op: "open", Path: "c/.tmp-*", Err: inner}, inner},
		{"other", inner, inner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := withoutPaths(tc.err); got != tc.want {
				t.Errorf("withoutPaths = %v; want %v", got, tc.want)
			}
		})
	}
}

// A nil saver, for a cache a run has none of, saves nothing and loses nothing.
func TestCacheSaver_Nil(t *testing.T) {
	var s *cacheSaver
	s.save("Show")
	if s.finish() || s.saveOnce() {
		t.Error("a nil saver reported a lost save")
	}
	if newShowSaver(nil) != nil || newRatingSaver(nil, "x") != nil || newTitleSaver(nil) != nil ||
		newIgnoreSaver(nil) != nil || newRetrySaver(nil) != nil {
		t.Error("a saver for a nil cache isn't nil")
	}
}

// writeCacheFile writes content to file in cfgHome's config dir, or makes
// file a directory when content is nil, and returns its path.
func writeCacheFile(t *testing.T, cfgHome, file string, content []byte) string {
	t.Helper()
	path := filepath.Join(cfgHome, "mkv-tvshow", file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	if content == nil {
		err = os.Mkdir(path, 0o755)
	} else {
		err = os.WriteFile(path, content, 0o644)
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// checkLoadError checks out reports a failed load of path, and gives the
// corrupt-file advice, naming what deleting it loses, only when corrupt.
func checkLoadError(t *testing.T, out string, err error, path string, corrupt bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("succeeded:\n%s", out)
	}
	if !strings.Contains(out, "Error loading "+path+": ") {
		t.Errorf("no load error naming %s:\n%s", path, out)
	}
	if got := strings.Contains(out, "It is corrupt") && strings.Contains(out, "Deleting it loses "); got != corrupt {
		t.Errorf("corrupt advice = %v; want %v:\n%s", got, corrupt, out)
	}
	if !corrupt && strings.Contains(out, "delete it") {
		t.Errorf("'delete it' advice for a cache that isn't corrupt:\n%s", out)
	}
}

// An unloadable cache stops --clean, with advice that suits the error. Only a
// corrupt file gets the "delete it" advice.
func TestMain_Clean_UnloadableCacheExits(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		content    []byte // nil makes file a directory
		corrupt    bool
	}{
		{"corrupt title cache", "title_cache.json", []byte("{not json"), true},
		{"truncated show cache", "show_cache.json", []byte(`{"a": "1"`), true},
		{"empty show cache", "show_cache.json", []byte{}, true},
		{"show cache of the wrong type", "show_cache.json", []byte(`["a"]`), true},
		{"rating cache of the wrong type", "rating_cache.json", []byte(`{"shows": []}`), true},
		{"unreadable rating cache", "rating_cache.json", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			path := writeCacheFile(t, cfgHome, tc.file, tc.content)

			out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

			checkLoadError(t, out, err, path, tc.corrupt)
		})
	}
}

// --reset-rating and --set-rating stop on a cache they can't load.
func TestMain_RatingModes_UnloadableCacheExits(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		file string
	}{
		{"reset show cache", []string{"--reset-rating", "Show"}, "show_cache.json"},
		{"reset rating cache", []string{"--reset-rating", "Show"}, "rating_cache.json"},
		{"set show cache", []string{"--set-rating", "R16"}, "show_cache.json"},
		{"set rating cache", []string{"--set-rating", "R16"}, "rating_cache.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			if tc.file != "show_cache.json" {
				writeCacheFile(t, cfgHome, "show_cache.json", []byte(`{"Show": "200"}`))
			}
			path := writeCacheFile(t, cfgHome, tc.file, []byte("{not json"))

			out, err := runMain(t, t.TempDir(), cfgHome, tc.args...)

			checkLoadError(t, out, err, path, true)
		})
	}
}

// The full run names show_cache.json when it can't load it.
func TestMain_UnloadableShowCacheNamed(t *testing.T) {
	cfgHome := t.TempDir()
	path := writeCacheFile(t, cfgHome, "show_cache.json", []byte("{not json"))

	out, err := runMainEnv(t, t.TempDir(), cfgHome, []string{"TMDB_API_KEY=unused"})

	checkLoadError(t, out, err, path, true)
}
