package main

import (
	"os"
	"path/filepath"
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

	removed, err := removeStalePatterns(data, root)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	for _, k := range []string{"kept.mkv", filepath.Join(root, "kept.mkv")} {
		if _, ok := data.FilePatterns[k]; !ok {
			t.Errorf("entry %q removed, want kept", k)
		}
	}
}

func TestRemoveStalePatterns_UnreadableRootRemovesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "offline")
	data := &IgnoreData{FilePatterns: map[string][]string{"video.mkv": {"a"}}}

	removed, err := removeStalePatterns(data, root)
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want not-exist", err)
	}
	if removed != 0 || len(data.FilePatterns) != 1 {
		t.Errorf("removed %d, %d entries left; want 0 removed, 1 left", removed, len(data.FilePatterns))
	}
}
