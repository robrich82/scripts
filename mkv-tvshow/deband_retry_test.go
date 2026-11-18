package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// debandRenameRun runs --deband-only over one show holding a three-episode
// file, which it renames to S01E01-E03, with retry as its retry list. With
// listed, the file is on the list first with one failed run.
func debandRenameRun(t *testing.T, retry *RetryList, listed bool) (oldPath, newPath string, stats Stats) {
	t.Helper()
	dir := showLibrary(t, "Banded", "Banded - S01E01E02E03.mkv")
	oldPath = filepath.Join(dir, "Banded", "Banded - S01E01E02E03.mkv")
	newPath = filepath.Join(dir, "Banded", "Banded - S01E01-E03.mkv")
	if listed {
		retry.Add(oldPath)
		retry.Fail(oldPath)
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", extractTagsXML: "<Tags></Tags>"}
	manual := map[string]ManualEntry{"Banded": {DebandEpisodes: map[string]bool{"S01E01": true}}}
	p := NewProcessor(Config{MediaDir: dir}, nil, manual, nil, runner, nil, noopTimer{}, retry, nil, nil, nil, nil)
	captureStdout(t, func() { stats = p.RunDebandOnly() })
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("the file wasn't renamed: %v", err)
	}
	return oldPath, newPath, stats
}

// A file --deband-only renames keeps its retry entry, failed runs included,
// and the list is saved.
func TestRunDebandOnly_RenameMovesRetryEntry(t *testing.T) {
	retry := emptyRetryList(t)

	oldPath, newPath, stats := debandRenameRun(t, retry, true)

	if stats.LostSaves != 0 {
		t.Errorf("LostSaves = %d; want 0", stats.LostSaves)
	}
	saved, err := LoadRetryList(retry.path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Has(oldPath) {
		t.Error("the old path is still on the saved retry list")
	}
	if !saved.Has(newPath) || saved.Attempts(newPath) != 1 {
		t.Errorf("new path saved = %v with %d failed runs; want saved with 1", saved.Has(newPath), saved.Attempts(newPath))
	}
}

// A rename of a file that isn't on the retry list leaves the list file alone.
func TestRunDebandOnly_RenameOffRetryListWritesNothing(t *testing.T) {
	retry := emptyRetryList(t)

	debandRenameRun(t, retry, false)

	if _, err := os.Stat(retry.path); !os.IsNotExist(err) {
		t.Errorf("pending_dates.json was written (stat err %v); want it untouched", err)
	}
}

// A retry list that can't be saved after a move is reported and counted.
func TestRunDebandOnly_RetryListSaveFailure(t *testing.T) {
	old := beforeFinalSave
	beforeFinalSave = func() {}
	t.Cleanup(func() { beforeFinalSave = old })
	retry := emptyRetryList(t)
	if err := retry.Save(); err != nil {
		t.Fatal(err)
	}
	unwritable(t, retry.path)

	var stats Stats
	out := captureOutput(t, func() { _, _, stats = debandRenameRun(t, retry, true) })

	if stats.LostSaves != 1 {
		t.Errorf("LostSaves = %d; want 1", stats.LostSaves)
	}
	if !strings.Contains(out, "Failed to save retry list") {
		t.Errorf("output lacks the lost-save message:\n%s", out)
	}
}

// --deband-only stops on a retry list it can't load.
func TestMain_DebandOnlyLoadsRetryList(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "pending_dates.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runMain(t, t.TempDir(), cfgHome, "--deband-only")

	if err == nil || !strings.Contains(out, "Error loading "+filepath.Join(cfg, "pending_dates.json")) {
		t.Errorf("--deband-only = %v; want the retry list's load error:\n%s", err, out)
	}
}
