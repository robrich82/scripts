package main

import (
	"os"
	"path/filepath"
	"testing"
)

// --reset-rating resets a file whose name starts with ".." directly in the
// show folder, and leaves a sibling show's files alone.
func TestRunResetRating_FileNameStartingWithDots(t *testing.T) {
	cfg := useTempConfigDir(t)
	media := t.TempDir()
	show := filepath.Join(media, "Show (2020)")
	other := filepath.Join(media, "Show (2020) Extras")
	for _, d := range []string{show, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dotted := filepath.Join(show, "...Special.mkv")
	episode := filepath.Join(show, "S01E01.mkv")
	outside := filepath.Join(other, "S01E01.mkv")

	shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
	shows.Set(show, "100")
	mustSave(t, shows.Save())
	ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	for _, f := range []string{dotted, episode, outside} {
		ratings.MarkApplied(f, RatingPG)
	}
	mustSave(t, ratings.Save())

	captureStdout(t, func() { runResetRating("Show (2020)", media) })

	got, err := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.IsApplied(dotted, RatingPG) || got.IsApplied(episode, RatingPG) {
		t.Error("the show's files are still marked applied")
	}
	if !got.IsApplied(outside, RatingPG) {
		t.Error("a file outside the show folder was reset")
	}
}
