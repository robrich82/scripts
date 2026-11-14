package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runShows sets up one episode per show under a fresh media dir and runs a
// full pass over them, with every show in the show cache and TMDB rating PG.
func runShows(t *testing.T, cache *ShowCache, rc *RatingCache, plex PlexClient, prompter Prompter, shows ...string) string {
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
		&captureFileTimer{}, emptyRetryList(t), rc, []RatingSource{tmdb}, plex, nil)
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

// A cache --clean can't save is reported, left out of the total, and makes
// --clean exit 1; the other caches are still cleaned.
func TestMain_Clean_DependentCacheSaveFails(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := staleCaches(t, cfgHome)
	readOnly(t, filepath.Join(cfg, "title_cache.json"))

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

	if err == nil {
		t.Errorf("--clean succeeded:\n%s", out)
	}
	for _, want := range []string{
		"Error saving title_cache.json: ",
		"Nothing was removed from title_cache.json.",
		"pending_dates.json:   removed 1 stale entry",
		"Total: 3 stale entries removed.", // show, rating and pending; not title
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "title_cache.json:     removed") {
		t.Errorf("an unsaved removal was reported:\n%s", out)
	}
	if retries, _ := LoadRetryList(filepath.Join(cfg, "pending_dates.json")); len(retries.data) != 0 {
		t.Errorf("pending_dates.json still has %v", retries.data)
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
	if !strings.Contains(out, "Error saving show_cache.json: ") || !strings.Contains(out, "Nothing was removed from show_cache.json.") {
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

// An unloadable cache stops --clean, with advice that suits the error. Only a
// corrupt file gets the "delete it" advice.
func TestMain_Clean_UnloadableCacheExits(t *testing.T) {
	for _, tc := range []struct {
		name, file, content string // content "" with dir true makes file a directory
		dir                 bool
		hint                string
	}{
		{"corrupt title cache", "title_cache.json", "{not json", false, "It is corrupt"},
		{"truncated show cache", "show_cache.json", `{"a": "1"`, false, "It is corrupt"},
		{"empty show cache", "show_cache.json", "", false, "It is corrupt"},
		{"unreadable rating cache", "rating_cache.json", "", true, "Close any program that has it open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			path := filepath.Join(cfgHome, "mkv-tvshow", tc.file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			var err error
			if tc.dir {
				err = os.Mkdir(path, 0o755)
			} else {
				err = os.WriteFile(path, []byte(tc.content), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}

			out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

			if err == nil {
				t.Fatalf("--clean succeeded:\n%s", out)
			}
			if !strings.Contains(out, "Error loading "+tc.file) || !strings.Contains(out, tc.hint) {
				t.Errorf("want the load error and %q:\n%s", tc.hint, out)
			}
			if !tc.dir != strings.Contains(out, "delete it") {
				t.Errorf("'delete it' advice should appear only for a corrupt cache:\n%s", out)
			}
		})
	}
}

// The full run names show_cache.json when it can't load it.
func TestMain_UnloadableShowCacheNamed(t *testing.T) {
	cfgHome := t.TempDir()
	path := filepath.Join(cfgHome, "mkv-tvshow", "show_cache.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runMainEnv(t, t.TempDir(), cfgHome, []string{"TMDB_API_KEY=unused"})

	if err == nil || !strings.Contains(out, "Error loading show_cache.json") || !strings.Contains(out, "It is corrupt") {
		t.Errorf("run = %v; want the named load error:\n%s", err, out)
	}
}
