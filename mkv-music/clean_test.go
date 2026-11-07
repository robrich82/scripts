package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemoveStalePatterns_RemovesMissingVideos(t *testing.T) {
	root := musicDir(t, "kept.mkv")
	data := &IgnoreData{FilePatterns: map[string][]string{
		"kept.mkv":                         {"a"},
		"gone.mkv":                         {"b"},
		filepath.Join(root, "kept.mkv"):    {"c"},
		filepath.Join(root, "missing.mkv"): {"d"},
	}}

	if removed := removeStalePatterns(data, root); removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	for _, k := range []string{"kept.mkv", filepath.Join(root, "kept.mkv")} {
		if _, ok := data.FilePatterns[k]; !ok {
			t.Errorf("entry %q removed, want kept", k)
		}
	}
}

func TestRemoveStalePatterns_UnreadableRootKeepsLegacyEntries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "offline")
	data := &IgnoreData{FilePatterns: map[string][]string{"video.mkv": {"a"}}}

	var removed int
	out := captureStdout(t, func() { removed = removeStalePatterns(data, root) })
	if removed != 0 || len(data.FilePatterns) != 1 {
		t.Errorf("removed %d, %d entries left; want 0 removed, 1 left", removed, len(data.FilePatterns))
	}
	if !strings.Contains(out, root) {
		t.Errorf("output doesn't name the unreadable root %s:\n%s", root, out)
	}
}

func TestRemoveStalePatterns_OfflineDriveKeepsEntries(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive letters are Windows-only")
	}
	drive := ""
	for c := 'Z'; c >= 'D'; c-- {
		if _, err := os.Stat(string(c) + `:\`); err != nil {
			drive = string(c) + ":"
			break
		}
	}
	if drive == "" {
		t.Skip("no unused drive letter")
	}
	name := drive + `\Music\Artist\video.mkv`
	data := &IgnoreData{FilePatterns: map[string][]string{name: {"a"}}}

	if removed := removeStalePatterns(data, t.TempDir()); removed != 0 {
		t.Errorf("removed %d entries for offline drive %s, want 0", removed, drive)
	}
}
