package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// unusedDrive returns a drive letter with no volume behind it, standing in for
// a media drive or share that is offline.
func unusedDrive(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("drive letters are Windows-only")
	}
	for c := 'Z'; c >= 'D'; c-- {
		if _, err := os.Stat(string(c) + `:\`); err != nil {
			return string(c) + ":"
		}
	}
	t.Skip("no unused drive letter")
	return ""
}

// useTempConfigDir points configDir at a fresh temp dir and returns it.
func useTempConfigDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("APPDATA", base)
	t.Setenv("XDG_CONFIG_HOME", base)
	dir := filepath.Join(base, "mkv-tvshow")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunClean_OfflineDriveKeepsEntries(t *testing.T) {
	drive := unusedDrive(t)
	cfg := useTempConfigDir(t)
	media := t.TempDir()

	keptDir := filepath.Join(media, "Kept Show")
	if err := os.MkdirAll(keptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keptFile := filepath.Join(keptDir, "S01E01.mkv")
	if err := os.WriteFile(keptFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	goneDir := filepath.Join(media, "Gone Show")
	goneFile := filepath.Join(goneDir, "S01E01.mkv")
	offlineDir := drive + `\TV\Offline Show`
	offlineFile := offlineDir + `\S01E01.mkv`

	shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
	shows.Set(offlineDir, "100")
	shows.Set(goneDir, "200")
	shows.Set(keptDir, "300")
	mustSave(t, shows.Save())

	titles, _ := LoadTitleCache(filepath.Join(cfg, "title_cache.json"))
	ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	retries, _ := LoadRetryList(filepath.Join(cfg, "pending_dates.json"))
	for _, f := range []string{offlineFile, goneFile, keptFile} {
		titles.Set(f, "title")
		ratings.MarkApplied(f, RatingPG)
		retries.Add(f)
	}
	ratings.SetShowRating("100", RatingPG)
	ratings.SetShowRating("200", RatingPG)
	mustSave(t, titles.Save())
	mustSave(t, ratings.Save())
	mustSave(t, retries.Save())

	ignore, _ := LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
	ignore.AddShow("100")
	ignore.AddShow("200")
	mustSave(t, ignore.Save())

	captureStdout(t, func() { runClean(false) })

	shows, _ = LoadCache(filepath.Join(cfg, "show_cache.json"))
	for folder, want := range map[string]bool{offlineDir: true, goneDir: false, keptDir: true} {
		if _, ok := shows.Get(folder); ok != want {
			t.Errorf("show_cache has %s = %v, want %v", folder, ok, want)
		}
	}

	titles, _ = LoadTitleCache(filepath.Join(cfg, "title_cache.json"))
	ratings, _ = LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	retries, _ = LoadRetryList(filepath.Join(cfg, "pending_dates.json"))
	for f, want := range map[string]bool{offlineFile: true, goneFile: false, keptFile: true} {
		if _, ok := titles.Get(f); ok != want {
			t.Errorf("title_cache has %s = %v, want %v", f, ok, want)
		}
		if ok := ratings.IsApplied(f, RatingPG); ok != want {
			t.Errorf("rating_cache applied has %s = %v, want %v", f, ok, want)
		}
		if ok := retries.Has(f); ok != want {
			t.Errorf("pending_dates has %s = %v, want %v", f, ok, want)
		}
	}

	for id, want := range map[string]bool{"100": true, "200": false} {
		if _, ok := ratings.GetShowRating(id); ok != want {
			t.Errorf("rating_cache show %s = %v, want %v", id, ok, want)
		}
	}
	ignore, _ = LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
	for id, want := range map[string]bool{"100": true, "200": false} {
		if ok := ignore.IsShowIgnored(id); ok != want {
			t.Errorf("missing_ignore show %s = %v, want %v", id, ok, want)
		}
	}
}

func mustSave(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A legacy show_cache key holds only a folder name, with nothing recording its
// library. --clean must keep it, and its show ID in the dependent caches, rather
// than resolve it against the current directory.
func TestRunClean_KeepsLegacyFolderNameKeys(t *testing.T) {
	cfg := useTempConfigDir(t)
	t.Chdir(t.TempDir()) // not the library root

	shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
	shows.Set("Legacy Show", "100")
	shows.Set(filepath.Join(t.TempDir(), "Gone Show"), "200")
	mustSave(t, shows.Save())

	ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	ratings.SetShowRating("100", RatingPG)
	mustSave(t, ratings.Save())
	ignore, _ := LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
	ignore.AddShow("100")
	mustSave(t, ignore.Save())

	captureStdout(t, func() { runClean(false) })

	shows, _ = LoadCache(filepath.Join(cfg, "show_cache.json"))
	if _, ok := shows.Get("Legacy Show"); !ok {
		t.Error("legacy folder-name key was removed")
	}
	if n := len(shows.AllEntries()); n != 1 {
		t.Errorf("show_cache has %d entries, want only the legacy key: %v", n, shows.AllEntries())
	}
	ratings, _ = LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	if _, ok := ratings.GetShowRating("100"); !ok {
		t.Error("rating_cache lost the legacy show's rating")
	}
	ignore, _ = LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
	if !ignore.IsShowIgnored("100") {
		t.Error("missing_ignore lost the legacy show")
	}
}
