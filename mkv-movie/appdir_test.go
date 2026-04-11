package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDir_ReturnsSubdir(t *testing.T) {
	dir, err := configDir()
	if err != nil {
		t.Fatalf("configDir: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), "/mkv-movie") {
		t.Errorf("configDir = %q, want a path ending in mkv-movie", dir)
	}
}

func TestConfigDir_IsAbsolute(t *testing.T) {
	dir, err := configDir()
	if err != nil {
		t.Fatalf("configDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("configDir = %q is not absolute", dir)
	}
}

func TestConfigDir_MatchesUserConfigDir(t *testing.T) {
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skip("os.UserConfigDir unavailable:", err)
	}
	want := filepath.Join(base, "mkv-movie")

	got, err := configDir()
	if err != nil {
		t.Fatalf("configDir: %v", err)
	}
	if got != want {
		t.Errorf("configDir = %q, want %q", got, want)
	}
}
