package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCache_FileNotExist(t *testing.T) {
	c, err := LoadCache(filepath.Join(t.TempDir(), "no_such_file.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := c.Get("anything"); ok {
		t.Error("expected empty cache")
	}
}

func TestLoadCache_ValidStringJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	os.WriteFile(path, []byte(`{"ShowA": "12345"}`), 0644)

	c, err := LoadCache(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	id, ok := c.Get("ShowA")
	if !ok || id != "12345" {
		t.Errorf("got %q, ok=%v; want \"12345\", true", id, ok)
	}
}

func TestLoadCache_NumericJSON(t *testing.T) {
	// Python-generated cache files store IDs as JSON numbers.
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	os.WriteFile(path, []byte(`{"ShowB": 67890}`), 0644)

	c, err := LoadCache(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	id, ok := c.Get("ShowB")
	if !ok || id != "67890" {
		t.Errorf("got %q, ok=%v; want \"67890\", true", id, ok)
	}
}

func TestLoadCache_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	os.WriteFile(path, []byte(`not json`), 0644)

	_, err := LoadCache(path)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestShowCache_SetAndGet(t *testing.T) {
	c := &ShowCache{data: make(map[string]string)}
	c.Set("My Show", "999")
	id, ok := c.Get("My Show")
	if !ok || id != "999" {
		t.Errorf("got %q, ok=%v; want \"999\", true", id, ok)
	}
}

func TestShowCache_GetMissing(t *testing.T) {
	c := &ShowCache{data: make(map[string]string)}
	_, ok := c.Get("Missing")
	if ok {
		t.Error("expected miss for unknown key")
	}
}

func TestShowCache_Save(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	c := &ShowCache{path: path, data: make(map[string]string)}
	c.Set("Show X", "42")

	if err := c.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Reload and verify
	c2, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache after Save failed: %v", err)
	}
	id, ok := c2.Get("Show X")
	if !ok || id != "42" {
		t.Errorf("after reload got %q, ok=%v; want \"42\", true", id, ok)
	}
}
