package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunClean_OfflineDriveKeepsEntries(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive letters are Windows-only")
	}
	drive := ""
	for c := 'Z'; c >= 'D'; c-- {
		if _, err := os.Stat(string(c) + `:\`); err != nil {
			drive = string(c) + ":"
			break
		}
	}
	if drive == "" {
		t.Skip("no unused drive letter")
	}

	base := t.TempDir()
	t.Setenv("APPDATA", base)
	cfg := filepath.Join(base, "mkv-movie")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(cfg, "movie_rating_cache.json")

	media := t.TempDir()
	kept := filepath.Join(media, "Kept (2020).mkv")
	if err := os.WriteFile(kept, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(media, "Gone (2020).mkv")
	offline := drive + `\Movies\Offline (2020).mkv`

	// The offline file is the only one mapped to movie 2, so its rating
	// survives only if --clean counts the offline file as present.
	ids := map[string]string{offline: "2", gone: "1", kept: "1"}
	cache, _ := LoadMovieRatingCache(cachePath)
	for f, id := range ids {
		cache.data.Applied[f] = string(RatingPG)
		cache.SetFileTMDBID(f, id)
		cache.Set(id, RatingPG)
	}
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, runClean)

	cache, err := LoadMovieRatingCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]bool{offline: true, gone: false, kept: true} {
		if _, ok := cache.data.Applied[f]; ok != want {
			t.Errorf("applied has %s = %v, want %v", f, ok, want)
		}
		if _, ok := cache.data.Files[f]; ok != want {
			t.Errorf("files has %s = %v, want %v", f, ok, want)
		}
	}
	if _, ok := cache.Get("2"); !ok {
		t.Error("the offline file's movie lost its rating")
	}
}
