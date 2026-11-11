package main

import (
	"encoding/json"
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
	cache.SetFileTMDBID(kept, "1")
	cache.MarkMetaApplied(gone, "2")
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, runClean)

	cache, err := LoadMovieRatingCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"1": true, "2": false, "3": false} {
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
	if cache.GetTMDBID("Kept:2020") != "1" || cache.GetTMDBID("Gone:2020") != "" || cache.GetTMDBID("Orphan:2001") != "" {
		t.Errorf("tmdb_ids = %v; want only Kept:2020", cache.data.TMDBIDs)
	}
	if _, ok := cache.data.Files[gone]; ok {
		t.Error("files still has the vanished file")
	}
	// 1 vanished path in files + 1 in meta_applied; movies 2 and 3 in four maps each.
	if !strings.Contains(out, "Removed 2 stale file entries") || !strings.Contains(out, "Removed 8 entries for movies no longer on disk") {
		t.Errorf("unexpected --clean summary:\n%s", out)
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
	if paths, movies := cache.CleanStalePaths(); paths != 0 || movies != 0 {
		t.Errorf("CleanStalePaths removed %d paths, %d movies; want nothing", paths, movies)
	}
	if got, _ := cache.Get("1"); got != RatingM {
		t.Errorf("rating = %q; want M kept", got)
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

func TestMovieRatingCache_SaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "movie_rating_cache.json")
	cache, _ := LoadMovieRatingCache(cachePath)
	cache.Set("1", RatingM)
	for range 2 {
		if err := cache.Save(); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %v; want only the cache (no leftover temp files)", entries)
	}
	reloaded, err := LoadMovieRatingCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get("1"); got != RatingM {
		t.Errorf("reloaded rating = %q; want M", got)
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
		walkAndProcess(dir, tmdb, nil, &fakeFileTimer{}, nil, cache, nil, false, time.Time{})
	})

	if len(tmdb.sawSaved) != 1 || !tmdb.sawSaved[0] {
		t.Errorf("Alpha's TMDB ID on disk while Beta was processed: %v; want saved", tmdb.sawSaved)
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
	main()
	os.Exit(0)
}

// A corrupt movie_rating_cache.json stops the run instead of silently
// starting from an empty cache and re-prompting for every rating.
func TestMain_CorruptRatingCache_Exits(t *testing.T) {
	cfgHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgHome, "mkv-movie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgHome, "mkv-movie", "movie_rating_cache.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunMainHelper$", "--", "--all")
	cmd.Dir = t.TempDir()
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "TMDB_API_KEY="), strings.HasPrefix(kv, "PLEX_"),
			strings.HasPrefix(kv, "APPDATA="), strings.HasPrefix(kv, "XDG_CONFIG_HOME="), strings.HasPrefix(kv, "HOME="):
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "MKV_MOVIE_RUN_MAIN=1", "TMDB_API_KEY=unused", "APPDATA="+cfgHome, "XDG_CONFIG_HOME="+cfgHome, "HOME="+cfgHome)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("run with a corrupt cache succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "Error loading movie_rating_cache.json") {
		t.Errorf("missing load error:\n%s", out)
	}
	if strings.Contains(string(out), "Scanning") {
		t.Errorf("run continued past the load error:\n%s", out)
	}
}
