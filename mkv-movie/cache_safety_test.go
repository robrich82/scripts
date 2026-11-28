package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --clean drops vanished files, then every ID-keyed entry no remaining file maps to.
func TestRunClean_PrunesMoviesNoFileMapsTo(t *testing.T) {
	base := t.TempDir()
	t.Setenv("APPDATA", base)
	t.Setenv("XDG_CONFIG_HOME", base)
	cfg := filepath.Join(base, "mkv-movie")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(cfg, "movie_rating_cache.json")

	media := t.TempDir()
	kept := writeMovieFile(t, media, "Kept (2020).mkv")
	gone := filepath.Join(media, "Gone (2020).mkv")

	cache, _ := LoadMovieRatingCache(cachePath)
	for id, title := range map[string]string{"1": "Kept:2020", "2": "Gone:2020", "3": "Orphan:2001"} {
		cache.SetTMDBID(title, id)
		cache.Set(id, RatingPG)
		cache.SetPlexRating(id, RatingPG)
		cache.SetStreamsApplied(id, "en")
	}
	cache.SetTMDBID("Gonzo:2020", "2") // a second key: listed once, by the first key
	cache.SetPlexRating("4", RatingPG) // no title:YEAR key to name it by
	cache.SetFileTMDBID(kept, "1")
	cache.SetFileTMDBID(gone, "2")
	cache.data.Applied[gone] = string(RatingPG)
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, runClean)

	cache, err := LoadMovieRatingCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"1": true, "2": false, "3": false, "4": false} {
		if _, ok := cache.Get(id); ok != want {
			t.Errorf("movies has %s = %v, want %v", id, ok, want)
		}
		if _, ok := cache.GetPlexRating(id); ok != want {
			t.Errorf("plex_ratings has %s = %v, want %v", id, ok, want)
		}
		if _, ok := cache.GetStreamsApplied(id); ok != want {
			t.Errorf("streams_applied has %s = %v, want %v", id, ok, want)
		}
	}
	if len(cache.data.TMDBIDs) != 1 || cache.GetTMDBID("Kept:2020") != "1" {
		t.Errorf("tmdb_ids = %v; want only Kept:2020", cache.data.TMDBIDs)
	}
	if _, ok := cache.data.Files[gone]; ok {
		t.Error("files still has the vanished file")
	}
	// The vanished file is in both files and applied but counts once.
	for _, want := range []string{
		"Removed 1 file no longer on disk.",
		"Plex state of 3 movies no file on disk maps to:\n  Gone (2020)\n  Orphan (2001)\n  TMDB ID 4\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--clean summary lacks %q:\n%s", want, out)
		}
	}

	if out := captureStdout(t, runClean); !strings.Contains(out, "Cache is clean; nothing to remove.") {
		t.Errorf("second --clean:\n%s", out)
	}
}

func TestMovieDisplayName(t *testing.T) {
	for _, tc := range []struct{ id, key, want string }{
		{"1", "Coco:2017", "Coco (2017)"},
		{"1", "Star Wars: A New Hope:1977", "Star Wars: A New Hope (1977)"},
		{"1", "Untitled:", "Untitled"},
		{"42", "", "TMDB ID 42"},
		{"Inception:2010", "", "Inception (2010)"}, // a legacy title:YEAR rating key
	} {
		if got := movieDisplayName(tc.id, tc.key); got != tc.want {
			t.Errorf("movieDisplayName(%q, %q) = %q; want %q", tc.id, tc.key, got, tc.want)
		}
	}
}

// A cache written before "files" existed maps files to IDs only through
// meta_applied; loading it must keep those movies through --clean.
func TestLoadMovieRatingCache_BackfillsFilesFromMetaApplied(t *testing.T) {
	media := t.TempDir()
	kept := writeMovieFile(t, media, "Kept (2020).mkv")
	cachePath := filepath.Join(t.TempDir(), "movie_rating_cache.json")
	legacy, _ := json.Marshal(map[string]any{
		"movies":       map[string]string{"1": "M"},
		"tmdb_ids":     map[string]string{"Kept:2020": "1"},
		"meta_applied": map[string]string{kept: "1"},
	})
	if err := os.WriteFile(cachePath, legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	cache, err := LoadMovieRatingCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if files, movies := cache.CleanStalePaths(); len(files) != 0 || len(movies) != 0 {
		t.Errorf("CleanStalePaths removed files %v, movies %v; want nothing", files, movies)
	}
	if got, _ := cache.Get("1"); got != RatingM {
		t.Errorf("rating = %q; want M kept", got)
	}

	// Saving moves the mapping into "files" and drops the retired map.
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	b, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved["meta_applied"]; ok {
		t.Errorf("saved cache still has meta_applied:\n%s", b)
	}
	if reloaded, err := LoadMovieRatingCache(cachePath); err != nil || reloaded.data.Files[kept] != "1" {
		t.Errorf("reloaded files[%s] = %q (err %v); want 1", kept, reloaded.data.Files[kept], err)
	}
}

// --reset-rating finds a movie's files through "files", so a file processed
// without MKVToolNix (never meta-applied) still has its applied state reset.
func TestResetForTMDBID_UsesFileMap(t *testing.T) {
	cache := newTestMovieCache(t)
	cache.SetFileTMDBID("/m/Coco (2017).mkv", "354912")
	cache.data.Applied["/m/Coco (2017).mkv"] = string(RatingPG)
	cache.Set("354912", RatingPG)

	paths := cache.ResetForTMDBID("354912")

	if len(paths) != 1 || paths[0] != "/m/Coco (2017).mkv" {
		t.Errorf("reset paths = %v; want the one mapped file", paths)
	}
	if _, ok := cache.data.Applied["/m/Coco (2017).mkv"]; ok {
		t.Error("applied entry survived the reset")
	}
}

// Save writes a new file and renames it over the old one, rather than
// rewriting the old file in place: a hard link to the old file keeps the old bytes.
func TestMovieRatingCache_SaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "movie_rating_cache.json")
	cache, _ := LoadMovieRatingCache(cachePath)
	cache.Set("1", RatingPG)
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "old.json")
	if err := os.Link(cachePath, link); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}

	cache.Set("1", RatingM)
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(link); string(got) != string(old) {
		t.Errorf("the old file was rewritten in place:\n%s", got)
	}
	assertOnlyEntry(t, dir, "movie_rating_cache.json")
	reloaded, err := LoadMovieRatingCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get("1"); got != RatingM {
		t.Errorf("reloaded rating = %q; want M", got)
	}
}

// A failed save removes its temp file.
func TestAtomicWriteFile_FailureLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "movie_rating_cache.json")
	if err := os.Mkdir(target, 0o755); err != nil { // a directory can't be renamed over
		t.Fatal(err)
	}
	if err := atomicWriteFile(target, []byte("{}")); err == nil {
		t.Fatal("write over a directory succeeded")
	}
	assertOnlyEntry(t, dir, "movie_rating_cache.json")
}

func assertOnlyEntry(t *testing.T, dir, name string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != name {
		t.Errorf("dir has %v; want only %s (no leftover temp files)", entries, name)
	}
}

// diskCheckingTMDB reads the cache file from disk on every search after the
// first, recording whether the earlier files' TMDB IDs were already saved.
type diskCheckingTMDB struct {
	t         *testing.T
	cachePath string
	calls     int
	sawSaved  []bool
}

func (d *diskCheckingTMDB) SearchAndFetch(title, _ string) (string, MovieDetails, error) {
	d.calls++
	if d.calls > 1 {
		onDisk, err := LoadMovieRatingCache(d.cachePath)
		if err != nil {
			d.t.Fatal(err)
		}
		d.sawSaved = append(d.sawSaved, onDisk.GetTMDBID("Alpha:2001") == "101")
	}
	if title == "Alpha" {
		return "101", MovieDetails{Title: "Alpha", ReleaseDate: "2001-01-01"}, nil
	}
	return "202", MovieDetails{Title: "Beta", ReleaseDate: "2002-01-01"}, nil
}

func (d *diskCheckingTMDB) GetMovieDetails(string) (MovieDetails, error) {
	return MovieDetails{}, nil
}

// The cache is saved after each file, so Ctrl+C mid-run keeps what was resolved.
func TestWalkAndProcess_SavesAfterEachFile(t *testing.T) {
	dir := t.TempDir()
	writeMovieFile(t, dir, "Alpha (2001).mkv")
	writeMovieFile(t, dir, "Beta (2002).mkv")
	cachePath := filepath.Join(t.TempDir(), "movie_rating_cache.json")
	cache, _ := LoadMovieRatingCache(cachePath)
	tmdb := &diskCheckingTMDB{t: t, cachePath: cachePath}

	captureStdout(t, func() {
		walkAndProcess(dir, tmdb, nil, &fakeFileTimer{}, nil, cache, nil, time.Time{})
	})

	if len(tmdb.sawSaved) != 1 || !tmdb.sawSaved[0] {
		t.Errorf("Alpha's TMDB ID on disk while Beta was processed: %v; want saved", tmdb.sawSaved)
	}
	// The last file's save comes after it is processed, and each file is mapped
	// to its ID even with no applier (so nothing is meta-applied).
	onDisk, err := LoadMovieRatingCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := onDisk.GetTMDBID("Beta:2002"); got != "202" {
		t.Errorf("Beta's TMDB ID on disk = %q; want 202", got)
	}
	for name, id := range map[string]string{"Alpha (2001).mkv": "101", "Beta (2002).mkv": "202"} {
		if got := onDisk.data.Files[filepath.Join(dir, name)]; got != id {
			t.Errorf("files[%s] = %q; want %s", name, got, id)
		}
	}
}

// stubTMDB finds every movie as ID "1", calling onSearch (if set) first.
type stubTMDB struct{ onSearch func(title string) }

func (s stubTMDB) SearchAndFetch(title, year string) (string, MovieDetails, error) {
	if s.onSearch != nil {
		s.onSearch(title)
	}
	return "1", MovieDetails{Title: title, ReleaseDate: year + "-01-01"}, nil
}

func (stubTMDB) GetMovieDetails(string) (MovieDetails, error) {
	return MovieDetails{}, nil
}

// setBeforeFinalSave replaces walkAndProcess's pre-retry pause for one test.
func setBeforeFinalSave(t *testing.T, f func()) {
	old := beforeFinalSave
	beforeFinalSave = f
	t.Cleanup(func() { beforeFinalSave = old })
}

// A save that keeps failing is reported once, not after every file, and again
// at the end of the run.
func TestWalkAndProcess_PersistentSaveFailureReportedOnce(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	dir := t.TempDir()
	for _, name := range []string{"Alpha (2001).mkv", "Beta (2002).mkv", "Gamma (2003).mkv"} {
		writeMovieFile(t, dir, name)
	}
	cache, _ := LoadMovieRatingCache(filepath.Join(t.TempDir(), "missing", "movie_rating_cache.json"))

	out := captureStdout(t, func() {
		walkAndProcess(dir, stubTMDB{}, nil, &fakeFileTimer{}, nil, cache, nil, time.Time{})
	})

	if n := strings.Count(out, "Failed to save rating cache"); n != 1 {
		t.Errorf("save failure printed %d times; want once:\n%s", n, out)
	}
	if !strings.Contains(out, "The rating cache was never saved") {
		t.Errorf("no end-of-run warning:\n%s", out)
	}
}

// When the last file's save fails, the cache is saved once more after the
// walk; the warning names the last file saved if that fails too.
func TestWalkAndProcess_LastSaveRetried(t *testing.T) {
	for _, recovers := range []bool{true, false} {
		dir := t.TempDir()
		writeMovieFile(t, dir, "Alpha (2001).mkv")
		writeMovieFile(t, dir, "Beta (2002).mkv")
		cfg := filepath.Join(t.TempDir(), "cfg")
		if err := os.Mkdir(cfg, 0o755); err != nil {
			t.Fatal(err)
		}
		cache, _ := LoadMovieRatingCache(filepath.Join(cfg, "movie_rating_cache.json"))
		tmdb := stubTMDB{onSearch: func(title string) {
			if title == "Beta" { // Alpha saved; Beta's save fails
				if err := os.RemoveAll(cfg); err != nil {
					t.Fatal(err)
				}
			}
		}}
		setBeforeFinalSave(t, func() {
			if recovers {
				if err := os.Mkdir(cfg, 0o755); err != nil {
					t.Fatal(err)
				}
			}
		})

		out := captureStdout(t, func() {
			walkAndProcess(dir, tmdb, nil, &fakeFileTimer{}, nil, cache, nil, time.Time{})
		})

		if recovers {
			onDisk, err := LoadMovieRatingCache(filepath.Join(cfg, "movie_rating_cache.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "[i] Rating cache saved.") || strings.Contains(out, "lost") || onDisk.GetTMDBID("Beta:2002") != "1" {
				t.Errorf("retry should save Beta and report it:\n%s", out)
			}
		} else if !strings.Contains(out, "last saved after Alpha (2001).mkv; later ratings") {
			t.Errorf("warning should name the last file saved:\n%s", out)
		}
	}
}

// A file whose cached TMDB ID can't be fetched is still mapped to it, so
// --clean keeps the movie.
func TestProcessFile_CachedIDFetchErrorKeepsFileMapping(t *testing.T) {
	path := writeMovieFile(t, t.TempDir(), "Coco (2017).mkv")
	cache := newTestMovieCache(t)
	cache.SetTMDBID("Coco:2017", "354912")

	captureStdout(t, func() {
		processFile(path, &fakeTMDBMovieClient{err: errors.New("timeout")}, nil, &fakeFileTimer{}, nil, cache, nil)
	})

	if got := cache.data.Files[path]; got != "354912" {
		t.Errorf("files[path] = %q; want 354912", got)
	}
}

// Once a save succeeds again, the run says so and gives no end-of-run warning.
func TestWalkAndProcess_SaveRecovers(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Alpha (2001).mkv", "Beta (2002).mkv"} {
		writeMovieFile(t, dir, name)
	}
	cfg := filepath.Join(t.TempDir(), "missing")
	cache, _ := LoadMovieRatingCache(filepath.Join(cfg, "movie_rating_cache.json"))
	tmdb := stubTMDB{onSearch: func(title string) {
		if title == "Beta" {
			if err := os.MkdirAll(cfg, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}}

	out := captureStdout(t, func() {
		walkAndProcess(dir, tmdb, nil, &fakeFileTimer{}, nil, cache, nil, time.Time{})
	})

	if !strings.Contains(out, "Failed to save rating cache") || !strings.Contains(out, "[i] Rating cache saved.") {
		t.Errorf("want a failure then a recovery notice:\n%s", out)
	}
	if strings.Contains(out, "lost") {
		t.Errorf("end-of-run warning after a successful save:\n%s", out)
	}
}

// TestRunMainHelper is the child side of runMain: it runs main() with the
// arguments after "--" when the test binary is re-executed with MKV_MOVIE_RUN_MAIN=1.
func TestRunMainHelper(t *testing.T) {
	if os.Getenv("MKV_MOVIE_RUN_MAIN") != "1" {
		t.Skip("helper process only")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Args = append([]string{"mkv-movie"}, args...)
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	// A fake plex.tv, or an address nothing listens on: no test reaches the real one.
	plexTVBaseURL = cmp.Or(os.Getenv("MKV_MOVIE_TEST_PLEXTV"), "http://127.0.0.1:1")
	plexTVCommunityURL = plexTVBaseURL
	main()
	os.Exit(0)
}

// An unloadable movie_rating_cache.json stops the run (and --clean) instead of
// silently starting from an empty cache and re-prompting for every rating. Only
// a corrupt file gets the "delete it" advice.
func TestMain_UnloadableRatingCache_Exits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		corrupt bool // else the cache path is a directory, so reading it fails
		hint    string
	}{
		{"corrupt", []string{"--all"}, true, "It is corrupt: restore it from a backup"},
		{"corrupt --clean", []string{"--clean"}, true, "It is corrupt: restore it from a backup"},
		{"unreadable", []string{"--all"}, false, "Close any program that has it open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			cachePath := filepath.Join(cfgHome, "mkv-movie", "movie_rating_cache.json")
			if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
				t.Fatal(err)
			}
			var err error
			if tc.corrupt {
				err = os.WriteFile(cachePath, []byte("{not json"), 0o644)
			} else {
				err = os.Mkdir(cachePath, 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestRunMainHelper$", "--"}, tc.args...)...)
			cmd.Dir = t.TempDir()
			var env []string
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "PLEX_") { // the rest are overridden below (last value wins)
					env = append(env, kv)
				}
			}
			cmd.Env = append(env, "MKV_MOVIE_RUN_MAIN=1", "TMDB_API_KEY=unused", "APPDATA="+cfgHome, "XDG_CONFIG_HOME="+cfgHome, "HOME="+cfgHome)
			out, err := cmd.CombinedOutput()

			if err == nil {
				t.Fatalf("run succeeded:\n%s", out)
			}
			if !strings.Contains(string(out), "Error loading movie_rating_cache.json") || !strings.Contains(string(out), tc.hint) {
				t.Errorf("want the load error and %q:\n%s", tc.hint, out)
			}
			if tc.corrupt != strings.Contains(string(out), "delete it") {
				t.Errorf("'delete it' advice should appear only for a corrupt cache:\n%s", out)
			}
			if strings.Contains(string(out), "Scanning") || strings.Contains(string(out), "Removed") {
				t.Errorf("run continued past the load error:\n%s", out)
			}
		})
	}
}
