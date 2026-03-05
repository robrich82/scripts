package main

import (
	"path/filepath"
	"testing"
)

func TestDetectLibrary_NoMatch(t *testing.T) {
	cfg := &GlobalConfig{
		Libraries: []Library{
			{Name: "TV", Path: "/media/TV"},
		},
	}
	lib := detectLibrary("/other/path", cfg)
	if lib != nil {
		t.Errorf("expected nil for non-matching cwd, got %+v", lib)
	}
}

func TestDetectLibrary_ExactMatch(t *testing.T) {
	cfg := &GlobalConfig{
		Libraries: []Library{
			{Name: "TV", Path: "/media/TV"},
		},
	}
	lib := detectLibrary("/media/TV", cfg)
	if lib == nil {
		t.Fatal("expected match, got nil")
	}
	if lib.Name != "TV" {
		t.Errorf("expected TV, got %q", lib.Name)
	}
}

func TestDetectLibrary_SubdirMatch(t *testing.T) {
	cfg := &GlobalConfig{
		Libraries: []Library{
			{Name: "TV", Path: "/media/TV"},
		},
	}
	lib := detectLibrary("/media/TV/ShowName/Season 1", cfg)
	if lib == nil || lib.Name != "TV" {
		t.Errorf("expected TV library for subdir, got %v", lib)
	}
}

func TestDetectLibrary_MostSpecificWins(t *testing.T) {
	cfg := &GlobalConfig{
		Libraries: []Library{
			{Name: "Media", Path: "/media"},
			{Name: "TV", Path: "/media/TV"},
		},
	}
	lib := detectLibrary("/media/TV/ShowName", cfg)
	if lib == nil || lib.Name != "TV" {
		t.Errorf("expected most-specific library TV, got %v", lib)
	}
}

func TestDetectLibrary_EmptyConfig(t *testing.T) {
	cfg := &GlobalConfig{}
	lib := detectLibrary("/any/path", cfg)
	if lib != nil {
		t.Errorf("expected nil for empty config, got %+v", lib)
	}
}

func TestResolveIETFMap_NilLib_UsesDefaults(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "show", "ep01.mkv")
	m := resolveIETFMap(file, root, nil)
	if m["eng"] != "en-US" {
		t.Errorf("expected en-US for eng, got %q", m["eng"])
	}
}

func TestResolveIETFMap_Applies_Override(t *testing.T) {
	root := t.TempDir()
	lib := &Library{
		Name: "TV",
		Path: root,
		Overrides: []DirOverride{
			{Path: "Agatha Christie's Poirot (1989)", IETF: map[string]string{"eng": "en-GB"}},
		},
	}

	// File inside the override directory → should get en-GB.
	inside := filepath.Join(root, "Agatha Christie's Poirot (1989)", "S01E01.mkv")
	m := resolveIETFMap(inside, root, lib)
	if m["eng"] != "en-GB" {
		t.Errorf("expected en-GB inside override dir, got %q", m["eng"])
	}
	// Other languages should retain their defaults.
	if m["jpn"] != "ja-JP" {
		t.Errorf("expected ja-JP for jpn, got %q", m["jpn"])
	}

	// File outside the override directory → should get en-US.
	outside := filepath.Join(root, "Another Show", "S01E01.mkv")
	m2 := resolveIETFMap(outside, root, lib)
	if m2["eng"] != "en-US" {
		t.Errorf("expected en-US outside override dir, got %q", m2["eng"])
	}
}

func TestResolveIETFMap_CaseInsensitive(t *testing.T) {
	root := t.TempDir()
	lib := &Library{
		Name: "TV",
		Path: root,
		Overrides: []DirOverride{
			{Path: "MyShow", IETF: map[string]string{"eng": "en-AU"}},
		},
	}
	// Path in different case.
	file := filepath.Join(root, "myshow", "ep01.mkv")
	m := resolveIETFMap(file, root, lib)
	if m["eng"] != "en-AU" {
		t.Errorf("expected en-AU for case-insensitive match, got %q", m["eng"])
	}
}
