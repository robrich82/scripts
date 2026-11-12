package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pathApplier records the file each Apply call tags.
type pathApplier struct{ paths []string }

func (a *pathApplier) Apply(filePath, _, _, _, _ string) error {
	a.paths = append(a.paths, filePath)
	return nil
}

// The --cutoff-date window alone decides what is retagged, as in mkv-tvshow:
// a previously tagged file modified inside it is retagged, one outside it is
// left alone, and retagging backdates the file out of the window, so the next
// run with the same cutoff does nothing.
func TestWalkAndProcess_CutoffWindowDecidesWhatIsRetagged(t *testing.T) {
	dir := t.TempDir()
	recent := writeMovieFile(t, dir, "Avengers Endgame (2019).mkv")
	old := writeMovieFile(t, dir, "Avengers Endgame (2019) - Extended.mkv")
	past := time.Date(2019, 4, 26, 0, 0, 0, 0, time.Local)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	cache, err := LoadMovieRatingCache(filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Both were tagged by an earlier run.
	cache.SetTMDBID("Avengers Endgame:2019", "12345")
	cache.SetFileTMDBID(recent, "12345")
	cache.SetFileTMDBID(old, "12345")

	now := time.Now()
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails()}

	applier := &pathApplier{}
	var stats Stats
	captureStdout(t, func() {
		stats = walkAndProcess(dir, tmdb, applier, &win32FileTimer{}, nil, cache, nil, cutoff)
	})
	if len(applier.paths) != 1 || applier.paths[0] != recent {
		t.Errorf("tagged %v; want only %s", applier.paths, recent)
	}
	if stats.Updated != 1 || stats.SkippedOld != 1 {
		t.Errorf("stats = %+v; want 1 updated, 1 older than the cutoff", stats)
	}
	info, err := os.Stat(recent)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(past) {
		t.Errorf("retagged file's mtime = %v; want the release date %v", info.ModTime(), past)
	}

	applier = &pathApplier{}
	captureStdout(t, func() {
		stats = walkAndProcess(dir, tmdb, applier, &win32FileTimer{}, nil, cache, nil, cutoff)
	})
	if len(applier.paths) != 0 || stats.SkippedOld != 2 {
		t.Errorf("second run tagged %v, stats %+v; want nothing in the window", applier.paths, stats)
	}
}
