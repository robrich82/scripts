package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles writes each name in dir with its own name as its content.
func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// assertContent fails unless path holds want.
func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("%s = %q, %v; want %q", filepath.Base(path), got, err, want)
	}
}

func TestRenameNoReplace(t *testing.T) {
	t.Run("free name", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, "a.mkv")
		if err := renameNoReplace(filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv")); err != nil {
			t.Fatal(err)
		}
		assertContent(t, filepath.Join(dir, "b.mkv"), "a.mkv")
	})
	t.Run("name taken", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, "a.mkv", "b.mkv")
		err := renameNoReplace(filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv"))
		if !errors.Is(err, errNameTaken) || !strings.Contains(err.Error(), "b.mkv already exists") {
			t.Errorf("err = %v; want b.mkv already exists", err)
		}
		assertContent(t, filepath.Join(dir, "a.mkv"), "a.mkv")
		assertContent(t, filepath.Join(dir, "b.mkv"), "b.mkv")
	})
	// On Windows the new spelling finds the file itself; elsewhere it's free.
	t.Run("case only", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, "pilot.mkv")
		if err := renameNoReplace(filepath.Join(dir, "pilot.mkv"), filepath.Join(dir, "Pilot.mkv")); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != "Pilot.mkv" {
			t.Errorf("dir = %v, %v; want only Pilot.mkv", entries, err)
		}
	})
}

// --title-check doesn't offer a rename onto another file's name, so a second
// copy of the episode under the TMDB name survives.
func TestTitleCheck_RenameOntoExistingFile_Refused(t *testing.T) {
	dir, oldPath, newPath := renameShow(t)
	writeFiles(t, filepath.Dir(newPath), filepath.Base(newPath))
	if err := os.WriteFile(oldPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	retry := emptyRetryList(t)
	retry.Add(oldPath)
	tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 2, Name: "New Title"}}}}
	tc := NewTitleChecker(Config{MediaDir: dir}, cacheWith(t, map[string]string{"Show": "999"}), nil, tmdb,
		&assertNoAskPrompter{t}, nil, newNullTitleCache(t), noopTimer{}, retry)

	out := captureOutput(t, tc.Run)

	assertContent(t, oldPath, "old")
	assertContent(t, newPath, filepath.Base(newPath))
	if !strings.Contains(out, "Not renaming: Show - S01E02 - New Title.mkv already exists") {
		t.Errorf("output doesn't explain the refusal:\n%s", out)
	}
	if !retry.Has(oldPath) {
		t.Error("retry entry moved off the file that wasn't renamed")
	}
}

// A normal run doesn't rename a multi-episode file onto another file's name:
// it tags the file under its own name and says why.
func TestNormalizeFilename_NameTaken_KeepsBoth(t *testing.T) {
	dir := t.TempDir()
	name, taken := "Show - S01E01E02E03.mkv", "Show - S01E01-E03.mkv"
	writeFiles(t, dir, name, taken)
	p := &Processor{}

	var got, gotPath string
	out := captureStdout(t, func() { got, gotPath = p.normalizeFilename(dir, name) })

	if got != name || gotPath != filepath.Join(dir, name) {
		t.Errorf("got %q, %q; want the original name", got, gotPath)
	}
	assertContent(t, filepath.Join(dir, name), name)
	assertContent(t, filepath.Join(dir, taken), taken)
	if !strings.Contains(out, "Not renaming "+name) || !strings.Contains(out, taken+" already exists") {
		t.Errorf("output doesn't explain the skipped rename:\n%s", out)
	}
}
