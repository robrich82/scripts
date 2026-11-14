package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// runShows sets up one episode per show under a fresh media dir and runs a
// full pass over them, with every show in the show cache and TMDB rating PG.
func runShows(t *testing.T, cache *ShowCache, rc *RatingCache, plex PlexClient, prompter Prompter, shows ...string) string {
	t.Helper()
	return runShowsRetry(t, emptyRetryList(t), cache, rc, plex, prompter, shows...)
}

// runShowsRetry is runShows with the given retry list.
func runShowsRetry(t *testing.T, retry *RetryList, cache *ShowCache, rc *RatingCache, plex PlexClient, prompter Prompter, shows ...string) string {
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
	return captureStdout(t, func() { p.Run() })
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

	runShows(t, cache, rc, &fakePlexClient{showRatingKey: "k1"}, &scriptedPrompter{}, "Show A")

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

	out := runShows(t, cache, rc, nil, &scriptedPrompter{}, "Show A", "Show B")

	if n := strings.Count(out, "[!] Failed to save rating cache"); n != 1 {
		t.Errorf("save failure printed %d times; want once:\n%s", n, out)
	}
	if !strings.Contains(out, "[!] The rating cache was never saved; this run's ratings and Plex state are lost") {
		t.Errorf("no end-of-run warning:\n%s", out)
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
		{"fails again", false, "[!] The show cache was never saved; this run's show matches are lost"},
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

			out := runShows(t, cache, emptyRatingCache(t), nil, &scriptedPrompter{answers: []string{"y"}}, "New Show")

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
			retry := &RetryList{path: filepath.Join(cfg, "pending_dates.json"), data: map[string]bool{}}
			setBeforeFinalSave(t, func() {
				if tc.recovers {
					if err := os.Mkdir(cfg, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			})

			out := runShowsRetry(t, retry, cacheWith(t, map[string]string{"Show A": "999"}), emptyRatingCache(t), nil,
				&scriptedPrompter{}, "Show A")

			lost := strings.Contains(out, "[!] Failed to save retry list "+retry.path+"; this run's changes to the episodes to retry are lost")
			if lost == tc.recovers {
				t.Errorf("loss reported = %v; want %v:\n%s", lost, !tc.recovers, out)
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

	out := captureStdout(t, checker.Run)

	for _, want := range []string{
		"[!] Failed to save title cache " + titles.path,
		"[!] The title cache was never saved; this run's full titles are lost",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
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

	out := captureStdout(t, func() {
		if err := checker.Run(); err != nil {
			t.Error(err)
		}
	})

	for _, want := range []string{
		"[!] Failed to save ignore list " + ignores.path,
		"[!] The ignore list was never saved; this run's ignore choices are lost",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
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

// A cache --clean can't save is reported with its path and a hint, left out of
// the total, and makes --clean exit 1; the other caches are still cleaned.
func TestMain_Clean_DependentCacheSaveFails(t *testing.T) {
	for _, file := range []string{"title_cache.json", "rating_cache.json", "pending_dates.json", "missing_ignore.json"} {
		t.Run(file, func(t *testing.T) {
			cfgHome := t.TempDir()
			cfg := staleCaches(t, cfgHome)
			readOnly(t, filepath.Join(cfg, file))

			out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

			if err == nil {
				t.Errorf("--clean succeeded:\n%s", out)
			}
			for _, want := range []string{
				"Error saving " + filepath.Join(cfg, file) + ": ",
				"Access was denied: the file is read-only",
				"Nothing was removed from " + file + ".",
				"Total: 4 stale entries removed.", // one per cache but this one
			} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(file) + `:\s+removed`).MatchString(out) {
				t.Errorf("an unsaved removal was reported:\n%s", out)
			}
			ratings, err := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, kept := ratings.GetShowRating("200"); kept != (file == "rating_cache.json") {
				t.Errorf("rating_cache.json kept show 200 = %v", kept)
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
	readOnly(t, filepath.Join(cfg, "title_cache.json"))

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
// rating cache and ignore list rather than treating them all as stale.
func TestMain_Clean_EmptyShowCacheKeepsShowIDs(t *testing.T) {
	for _, tc := range []struct {
		name, showCache string // "" leaves show_cache.json missing
	}{{"missing", ""}, {"empty", "{}"}} {
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

			out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

			if err != nil || !strings.Contains(out, "show_cache.json is empty or missing, so show IDs") {
				t.Errorf("--clean = %v; want the kept-IDs note:\n%s", err, out)
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

// The other caches are pruned against the cleaned show cache, so --clean stops
// at once if show_cache.json can't be saved.
func TestMain_Clean_ShowCacheSaveFailsStops(t *testing.T) {
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
	if strings.Contains(out, "stale entr") {
		t.Errorf("--clean went on past the show cache:\n%s", out)
	}
	ratings, err := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ratings.GetShowRating("200"); !ok {
		t.Error("rating_cache.json was pruned")
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
	readOnly(t, filepath.Join(cfg, "show_cache.json"))

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

	if err != nil || !strings.Contains(out, "All caches are clean; nothing to remove.") {
		t.Errorf("--clean = %v:\n%s", err, out)
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
