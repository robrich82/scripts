package main

import (
	"os"
	"path/filepath"
	"testing"
)

// makeShowDir creates mediaDir/show/file.mkv and returns the file path.
func makeShowDir(t *testing.T, mediaDir, show, file string) string {
	t.Helper()
	dir := filepath.Join(mediaDir, show)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestShowFoldersWithMKV_SkipsLooseFilesAndNonMKV(t *testing.T) {
	mediaDir := t.TempDir()
	makeShowDir(t, mediaDir, "Show A", "Show A - S01E01.mkv")
	makeShowDir(t, mediaDir, "Show B", "Show B - S01E01.MKV")
	makeShowDir(t, mediaDir, "Show C", "notes.txt")
	if err := os.WriteFile(filepath.Join(mediaDir, "loose.mkv"), []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := showFoldersWithMKV(mediaDir)
	want := []string{"Show A", "Show B"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("folder %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPlanSetRating_ResolvesCachedShowsAndReportsUnknown(t *testing.T) {
	mediaDir := t.TempDir()
	makeShowDir(t, mediaDir, "Show A", "Show A - S01E01.mkv")
	makeShowDir(t, mediaDir, "Show B", "Show B - S01E01.mkv")
	makeShowDir(t, mediaDir, "Show C", "Show C - S01E01.mkv")

	showCache := emptyCache(t)
	showCache.Set(filepath.Join(mediaDir, "Show A"), "111") // absolute-path key
	showCache.Set("Show B", "222")                          // legacy base-name key

	ratingCache := emptyRatingCache(t)
	ratingCache.SetShowRating("111", RatingPG)

	plans, unknown := planSetRating(mediaDir, showCache, ratingCache)

	if len(plans) != 2 {
		t.Fatalf("got %d plans, want 2: %+v", len(plans), plans)
	}
	if plans[0].TMDBID != "111" || plans[0].Current != RatingPG {
		t.Errorf("plan[0] = %+v, want TMDB 111 with current PG", plans[0])
	}
	if plans[1].TMDBID != "222" || plans[1].Current != RatingNone {
		t.Errorf("plan[1] = %+v, want TMDB 222 with no current rating", plans[1])
	}
	if len(unknown) != 1 || unknown[0] != "Show C" {
		t.Errorf("unknown = %v, want [Show C]", unknown)
	}
}

func TestApplySetRating_OverwritesShowAndClearsEpisodeOverrides(t *testing.T) {
	mediaDir := t.TempDir()
	epPath := makeShowDir(t, mediaDir, "Show A", "Show A - S01E01.mkv")

	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingPG)
	rc.SetEpisodeRating("111", 1, 1, RatingG)
	rc.SetEpisodeRating("111", 1, 2, RatingG)
	rc.SetShowRating("999", RatingM) // unrelated show, must be untouched
	rc.MarkApplied(epPath, RatingPG)

	plans := []setRatingPlan{{Folder: "Show A", TMDBID: "111", Current: RatingPG}}
	epsCleared, filesReset := applySetRating(rc, mediaDir, plans, RatingR16)

	if got, _ := rc.GetShowRating("111"); got != RatingR16 {
		t.Errorf("show rating = %q, want R16", got)
	}
	if _, ok := rc.GetEpisodeRating("111", 1, 1); ok {
		t.Error("episode override survived; it would take priority over the new show rating")
	}
	if epsCleared != 2 {
		t.Errorf("episodesCleared = %d, want 2", epsCleared)
	}
	if filesReset != 1 {
		t.Errorf("filesReset = %d, want 1", filesReset)
	}
	if rc.IsApplied(epPath, RatingPG) {
		t.Error("applied state survived; tags would not be rewritten")
	}
	if got, _ := rc.GetShowRating("999"); got != RatingM {
		t.Errorf("unrelated show rating = %q, want M", got)
	}
}
