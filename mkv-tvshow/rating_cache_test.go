package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRatingCache_FileNotExist(t *testing.T) {
	c, err := LoadRatingCache(filepath.Join(t.TempDir(), "rating_cache.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil cache")
	}
}

func TestLoadRatingCache_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rating_cache.json")
	content := `{"shows":{"123":"R16"},"episodes":{"123/S01E03":"M"}}`
	os.WriteFile(path, []byte(content), 0644)

	c, err := LoadRatingCache(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r, ok := c.GetShowRating("123")
	if !ok || r != RatingR16 {
		t.Errorf("GetShowRating(123) = (%q, %v); want (R16, true)", r, ok)
	}

	r, ok = c.GetEpisodeRating("123", 1, 3)
	if !ok || r != RatingM {
		t.Errorf("GetEpisodeRating(123,1,3) = (%q, %v); want (M, true)", r, ok)
	}
}

func TestLoadRatingCache_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rating_cache.json")
	os.WriteFile(path, []byte("{invalid"), 0644)

	_, err := LoadRatingCache(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestRatingCache_ShowRoundTrip(t *testing.T) {
	c, _ := LoadRatingCache(filepath.Join(t.TempDir(), "rating_cache.json"))

	if _, ok := c.GetShowRating("999"); ok {
		t.Error("expected miss before set")
	}
	c.SetShowRating("999", RatingPG)
	r, ok := c.GetShowRating("999")
	if !ok || r != RatingPG {
		t.Errorf("after Set, Get = (%q, %v); want (PG, true)", r, ok)
	}
}

func TestRatingCache_EpisodeRoundTrip(t *testing.T) {
	c, _ := LoadRatingCache(filepath.Join(t.TempDir(), "rating_cache.json"))

	if _, ok := c.GetEpisodeRating("42", 2, 7); ok {
		t.Error("expected miss before set")
	}
	c.SetEpisodeRating("42", 2, 7, RatingR13)
	r, ok := c.GetEpisodeRating("42", 2, 7)
	if !ok || r != RatingR13 {
		t.Errorf("after Set, Get = (%q, %v); want (R13, true)", r, ok)
	}
}

func TestRatingCache_SetRatingNone_NotStored(t *testing.T) {
	c, _ := LoadRatingCache(filepath.Join(t.TempDir(), "rating_cache.json"))
	c.SetShowRating("5", RatingNone)
	if _, ok := c.GetShowRating("5"); ok {
		t.Error("RatingNone should not be stored")
	}
}

func TestRatingCache_EpisodeKey_Format(t *testing.T) {
	key := episodeRatingKey("12345", 1, 3)
	if key != "12345/S01E03" {
		t.Errorf("got %q; want %q", key, "12345/S01E03")
	}
	key = episodeRatingKey("12345", 10, 12)
	if key != "12345/S10E12" {
		t.Errorf("got %q; want %q", key, "12345/S10E12")
	}
}

func TestRatingCache_Save(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rating_cache.json")

	c, _ := LoadRatingCache(path)
	c.SetShowRating("1", RatingM)
	c.SetEpisodeRating("1", 2, 5, RatingR16)
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	c2, err := LoadRatingCache(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	r, ok := c2.GetShowRating("1")
	if !ok || r != RatingM {
		t.Errorf("reloaded show rating = (%q, %v); want (M, true)", r, ok)
	}
	r, ok = c2.GetEpisodeRating("1", 2, 5)
	if !ok || r != RatingR16 {
		t.Errorf("reloaded episode rating = (%q, %v); want (R16, true)", r, ok)
	}
}
