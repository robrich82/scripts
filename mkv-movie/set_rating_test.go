package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMovieFile creates an empty file named name inside dir.
func writeMovieFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func newTestMovieCache(t *testing.T) *MovieRatingCache {
	t.Helper()
	c, err := LoadMovieRatingCache(filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatalf("LoadMovieRatingCache: %v", err)
	}
	return c
}

func TestPlanSetRating_ResolvesCachedMoviesAndReportsUnknown(t *testing.T) {
	dir := t.TempDir()
	writeMovieFile(t, dir, "Coco (2017).mkv")
	writeMovieFile(t, dir, "Spirited Away (2001).mkv")
	writeMovieFile(t, dir, "Unprocessed (2020).mkv")
	writeMovieFile(t, dir, "cover.jpg")

	cache := newTestMovieCache(t)
	cache.SetTMDBID("Coco:2017", "354912")
	cache.SetTMDBID("Spirited Away:2001", "129")
	cache.Set("354912", RatingPG)

	plans, unknown := planSetRating(dir, cache)

	if len(plans) != 2 {
		t.Fatalf("got %d plans, want 2: %+v", len(plans), plans)
	}
	if plans[0].TMDBID != "354912" || plans[0].Current != RatingPG {
		t.Errorf("plan[0] = %+v, want TMDB 354912 with current PG", plans[0])
	}
	if plans[1].TMDBID != "129" || plans[1].Current != RatingNone {
		t.Errorf("plan[1] = %+v, want TMDB 129 with no current rating", plans[1])
	}
	if len(unknown) != 1 || unknown[0] != "Unprocessed (2020).mkv" {
		t.Errorf("unknown = %v, want [Unprocessed (2020).mkv]", unknown)
	}
	// Each resolved file is mapped to its movie, so --clean keeps the override.
	if got := cache.data.Files[filepath.Join(dir, "Coco (2017).mkv")]; got != "354912" {
		t.Errorf("files[Coco] = %q; want 354912", got)
	}
}

func TestPlanSetRating_DeduplicatesFilesSharingATMDBID(t *testing.T) {
	dir := t.TempDir()
	writeMovieFile(t, dir, "Coco (2017).mkv")
	sub := filepath.Join(dir, "4K")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeMovieFile(t, sub, "Coco (2017).mkv")

	cache := newTestMovieCache(t)
	cache.SetTMDBID("Coco:2017", "354912")

	plans, _ := planSetRating(dir, cache)
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1 (same TMDB ID): %+v", len(plans), plans)
	}
}

func TestApplySetRating_OverwritesRatingAndResetsAppliedState(t *testing.T) {
	dir := t.TempDir()
	moviePath := writeMovieFile(t, dir, "Coco (2017).mkv")

	cache := newTestMovieCache(t)
	cache.SetTMDBID("Coco:2017", "354912")
	cache.Set("354912", RatingPG)
	cache.SetPlexRating("354912", RatingPG)
	cache.MarkMetaApplied(moviePath, "354912")
	cache.Set("999", RatingM) // unrelated movie, must be untouched

	plans := []setRatingPlan{{TMDBID: "354912", Display: "Coco:2017", Current: RatingPG}}
	filesReset := applySetRating(cache, plans, RatingR16)

	if got, _ := cache.Get("354912"); got != RatingR16 {
		t.Errorf("rating = %q, want R16", got)
	}
	if filesReset != 1 {
		t.Errorf("filesReset = %d, want 1", filesReset)
	}
	if cache.IsMetaApplied(moviePath, "354912") {
		t.Error("meta-applied state survived; the LAW_RATING tag would not be rewritten")
	}
	if _, ok := cache.GetPlexRating("354912"); ok {
		t.Error("Plex rating survived; the new rating would not be pushed")
	}
	if got, _ := cache.Get("999"); got != RatingM {
		t.Errorf("unrelated rating = %q, want M", got)
	}
}
