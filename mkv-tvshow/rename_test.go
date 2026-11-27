package main

import (
	"os"
	"path/filepath"
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
		if err == nil || err.Error() != "b.mkv already exists" {
			t.Errorf("err = %v; want b.mkv already exists", err)
		}
		assertContent(t, filepath.Join(dir, "a.mkv"), "a.mkv")
		assertContent(t, filepath.Join(dir, "b.mkv"), "b.mkv")
	})
	t.Run("folder at the name", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, "a.mkv")
		if err := os.Mkdir(filepath.Join(dir, "b.mkv"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := renameNoReplace(filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv")); err == nil {
			t.Error("renamed onto a folder")
		}
		assertContent(t, filepath.Join(dir, "a.mkv"), "a.mkv")
	})
	// os.SameFile says a hard link is the same file, but renaming onto it
	// would drop one of its names (Windows) or do nothing (POSIX).
	t.Run("hard link", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, "a.mkv")
		if err := os.Link(filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv")); err != nil {
			t.Skip("no hard links here:", err)
		}
		if err := renameNoReplace(filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv")); err == nil {
			t.Error("renamed onto a hard link of itself")
		}
		assertContent(t, filepath.Join(dir, "a.mkv"), "a.mkv")
		assertContent(t, filepath.Join(dir, "b.mkv"), "a.mkv")
	})
	// On a case-insensitive filesystem the new spelling finds the file itself.
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
// copy of the episode under the TMDB name survives, and the file keeps its
// retry entry and cached title.
func TestTitleCheck_RenameOntoExistingFile_Refused(t *testing.T) {
	dir, oldPath, newPath := renameShow(t)
	writeFiles(t, filepath.Dir(newPath), filepath.Base(newPath))
	retry := emptyRetryList(t)
	retry.Add(oldPath)
	titles := titleCacheWith(t, map[string]string{oldPath: "Show - S01E02 - Old Title"})

	_, out := runRenameCheckWith(t, dir, retry, titles, &assertNoAskPrompter{t})

	assertContent(t, oldPath, "fake")
	assertContent(t, newPath, filepath.Base(newPath))
	assertContains(t, out, "[!] Not renaming: Show - S01E02 - New Title.mkv already exists")
	if !retry.Has(oldPath) || retry.Has(newPath) {
		t.Error("retry entry moved off the file that wasn't renamed")
	}
	if got, ok := titles.Get(oldPath); !ok || got != "Show - S01E02 - Old Title" {
		t.Errorf("cached title = %q, %v; want the old one kept", got, ok)
	}
}

// A normal run doesn't rename a multi-episode file onto another file's name:
// it tags the file under its own name, keeping its cached title, and says why.
func TestRun_MultiEpisodeRenameOntoExistingFile_Refused(t *testing.T) {
	dir := showLibrary(t, "Show A", "Show A - S01E01E02E03.mkv")
	show := filepath.Join(dir, "Show A")
	name, taken := "Show A - S01E01E02E03.mkv", "Show A - S01E01-E03.mkv"
	writeFiles(t, show, name, taken)
	oldPath := filepath.Join(show, name)
	const cached = "Show A - S01E01E02E03 - One + Two + Three"
	titles := titleCacheWith(t, map[string]string{oldPath: cached})
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	tmdb := &fakeTMDB{
		showDetails:  ShowDetails{FirstAirDate: "2022-01-01"},
		seasonResult: SeasonResult{AirDate: "2022-02-01", Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2022-03-01"}}},
	}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Show A": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, emptyRetryList(t),
		nil, nil, nil, titles)

	out := captureStdout(t, func() { p.Run() })

	assertContent(t, oldPath, name)
	assertContent(t, filepath.Join(show, taken), taken)
	assertContains(t, out, "[!] Not renaming "+name+": "+taken+" already exists")
	tagged := false
	for _, c := range runner.calls {
		tagged = tagged || c.filePath == oldPath
	}
	if !tagged {
		t.Errorf("%s wasn't tagged under its own name; calls = %v", name, runner.calls)
	}
	if got := savedTitles(t, titles); got[oldPath] != cached {
		t.Errorf("title cache on disk = %v; want %q kept under %s", got, cached, oldPath)
	}
}
