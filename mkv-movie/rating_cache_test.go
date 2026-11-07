package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMovieRatingCache_MissingFile_EmptyCache(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadMovieRatingCache(filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := c.Get("12345"); ok {
		t.Error("expected empty cache")
	}
}

func TestLoadMovieRatingCache_ValidJSON_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	if err := os.WriteFile(path, []byte(`{"movies":{"12345":"R16","title:2020":"PG"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := LoadMovieRatingCache(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r, ok := c.Get("12345"); !ok || r != RatingR16 {
		t.Errorf("Get(12345) = %v, %v; want R16, true", r, ok)
	}
	if r, ok := c.Get("title:2020"); !ok || r != RatingPG {
		t.Errorf("Get(title:2020) = %v, %v; want PG, true", r, ok)
	}
}

func TestLoadMovieRatingCache_MalformedJSON_Error(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`not json`), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadMovieRatingCache(path)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestMovieRatingCache_SetAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	c, _ := LoadMovieRatingCache(path)
	c.Set("99999", RatingM)
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	c2, err := LoadMovieRatingCache(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if r, ok := c2.Get("99999"); !ok || r != RatingM {
		t.Errorf("after reload Get(99999) = %v, %v; want M, true", r, ok)
	}
}

func TestMovieRatingCache_GetMissing_ReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	c, _ := LoadMovieRatingCache(filepath.Join(dir, "c.json"))
	r, ok := c.Get("missing")
	if ok || r != RatingNone {
		t.Errorf("expected RatingNone, false; got %v, %v", r, ok)
	}
}
