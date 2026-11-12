package main

import (
	"encoding/json"
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

// legacyTaggedCache loads a cache, as written before meta_applied was retired,
// in which every path in paths was tagged as testDetails' movie.
func legacyTaggedCache(t *testing.T, paths ...string) *MovieRatingCache {
	t.Helper()
	meta := make(map[string]string, len(paths))
	for _, p := range paths {
		meta[p] = "12345"
	}
	b, err := json.Marshal(map[string]any{
		"movies":       map[string]string{},
		"tmdb_ids":     map[string]string{"Avengers Endgame:2019": "12345"},
		"meta_applied": meta,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "movie_rating_cache.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := LoadMovieRatingCache(path)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

// The --cutoff-date window alone decides what is retagged, as in mkv-tvshow:
// a previously tagged file modified inside it is retagged, one outside it is
// left alone, and retagging backdates the file (to a release date before the
// cutoff) out of the window, so the next run with the same cutoff does nothing.
func TestWalkAndProcess_CutoffWindowDecidesWhatIsRetagged(t *testing.T) {
	cutoff := time.Now().Add(-time.Hour)
	dir := t.TempDir()
	recent := writeMovieFile(t, dir, "Avengers Endgame (2019).mkv")
	old := writeMovieFile(t, dir, "Avengers Endgame (2019) - Extended.mkv")
	past := time.Date(2019, 4, 26, 0, 0, 0, 0, time.Local)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	cache := legacyTaggedCache(t, recent, old) // both tagged by an earlier run
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

// Retagging a file doesn't re-push a rating or streams Plex already has from
// an earlier run; --overwrite-streams re-pushes the streams.
func TestProcessFile_RetagDoesNotRepushToPlex(t *testing.T) {
	for _, tc := range []struct {
		name             string
		overwriteStreams bool
		wantStreamSets   int
	}{
		{"default", false, 1},
		{"--overwrite-streams", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
			cache := makeCache(t)
			cache.Set("12345", RatingM)
			plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
			rr := newMovieRatingResolver(nil, cache, false, tc.overwriteStreams, plex, answerPrompter())
			details := testDetails()
			details.OriginalLanguage = "en"
			tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: details}
			applier := &fakeApplier{}

			captureStdout(t, func() {
				processFile(path, tmdb, applier, &fakeFileTimer{}, rr, cache, nil)
				processFile(path, tmdb, applier, &fakeFileTimer{}, rr, cache, nil)
			})
			if len(applier.calls) != 2 {
				t.Errorf("Apply calls = %d; want the file retagged both times", len(applier.calls))
			}
			if plex.ratingSets != 1 || plex.streamSets != tc.wantStreamSets {
				t.Errorf("SetContentRating %d, SetMovieStreams %d; want 1, %d", plex.ratingSets, plex.streamSets, tc.wantStreamSets)
			}
		})
	}
}
