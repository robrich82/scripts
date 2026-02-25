package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeDirConfig(t *testing.T, dir string, cfg DirConfig) {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dirConfigFile), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDirConfig_NoFile_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if cfg := loadDirConfig(dir); cfg != nil {
		t.Errorf("expected nil for missing config, got %+v", cfg)
	}
}

func TestLoadDirConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	writeDirConfig(t, dir, DirConfig{
		Overrides: []DirOverride{
			{Path: "ShowA", IETF: map[string]string{"eng": "en-GB"}},
		},
	})
	cfg := loadDirConfig(dir)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Overrides) != 1 || cfg.Overrides[0].IETF["eng"] != "en-GB" {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestResolveIETFMap_NilConfig_UsesDefaults(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "show", "ep01.mkv")
	m := resolveIETFMap(file, root, nil)
	if m["eng"] != "en-US" {
		t.Errorf("expected en-US for eng, got %q", m["eng"])
	}
}

func TestResolveIETFMap_Applies_Override(t *testing.T) {
	root := t.TempDir()
	cfg := &DirConfig{
		Overrides: []DirOverride{
			{Path: "Agatha Christie's Poirot (1989)", IETF: map[string]string{"eng": "en-GB"}},
		},
	}

	// File inside the override directory → should get en-GB.
	inside := filepath.Join(root, "Agatha Christie's Poirot (1989)", "S01E01.mkv")
	m := resolveIETFMap(inside, root, cfg)
	if m["eng"] != "en-GB" {
		t.Errorf("expected en-GB inside override dir, got %q", m["eng"])
	}
	// Other languages should retain their defaults.
	if m["jpn"] != "ja-JP" {
		t.Errorf("expected ja-JP for jpn, got %q", m["jpn"])
	}

	// File outside the override directory → should get en-US.
	outside := filepath.Join(root, "Another Show", "S01E01.mkv")
	m2 := resolveIETFMap(outside, root, cfg)
	if m2["eng"] != "en-US" {
		t.Errorf("expected en-US outside override dir, got %q", m2["eng"])
	}
}

func TestResolveIETFMap_CaseInsensitive(t *testing.T) {
	root := t.TempDir()
	cfg := &DirConfig{
		Overrides: []DirOverride{
			{Path: "MyShow", IETF: map[string]string{"eng": "en-AU"}},
		},
	}
	// Path in different case.
	file := filepath.Join(root, "myshow", "ep01.mkv")
	m := resolveIETFMap(file, root, cfg)
	if m["eng"] != "en-AU" {
		t.Errorf("expected en-AU for case-insensitive match, got %q", m["eng"])
	}
}
