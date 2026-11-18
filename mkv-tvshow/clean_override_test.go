package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A manual_data.json season override's ID goes with its show: --clean removes
// it with a gone show, and keeps it (even with --all-ids) while the show
// exists or another show still uses it.
func TestMain_Clean_SeasonOverrideIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want map[string]bool // show ID → rating and ignore kept
	}{
		{"default", []string{"--clean"}, map[string]bool{
			"100": true, "150": true, "200": false, "250": false, "350": true, "300": true, "400": true,
		}},
		{"--all-ids", []string{"--clean", "--all-ids"}, map[string]bool{
			"100": true, "150": true, "200": false, "250": false, "350": true, "300": false, "400": true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			cfg := filepath.Join(cfgHome, "mkv-tvshow")
			if err := os.MkdirAll(cfg, 0o755); err != nil {
				t.Fatal(err)
			}
			media := t.TempDir()
			if err := os.Mkdir(filepath.Join(media, "Show A"), 0o755); err != nil {
				t.Fatal(err)
			}
			shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
			shows.Set(filepath.Join(media, "Show A"), "100")
			shows.Set(filepath.Join(media, "Gone"), "200")
			mustSave(t, shows.Save())
			writeManualData(t, cfg, `{
				"Show A": {"overrides": {"S02": {"id": "150", "map_season": 1}}},
				"Gone":   {"overrides": {"S02": {"id": "250", "map_season": 1}, "S03": {"id": "400", "map_season": 1}}},
				"Other":  {"overrides": {"S02": {"id": "350", "map_season": 1}, "S03": {"id": "400", "map_season": 1}}}
			}`)
			ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
			ignores, _ := LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
			for id := range tc.want {
				ratings.SetShowRating(id, RatingPG)
				ignores.AddShow(id)
			}
			mustSave(t, ratings.Save())
			mustSave(t, ignores.Save())

			out, err := runMain(t, t.TempDir(), cfgHome, tc.args...)

			if err != nil {
				t.Fatalf("%v = %v:\n%s", tc.args, err, out)
			}
			ratings, _ = LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
			ignores, _ = LoadMissingIgnoreList(filepath.Join(cfg, "missing_ignore.json"))
			for id, want := range tc.want {
				if _, ok := ratings.GetShowRating(id); ok != want {
					t.Errorf("rating for show %s kept = %v; want %v", id, ok, want)
				}
				if got := ignores.IsShowIgnored(id); got != want {
					t.Errorf("ignore for show %s kept = %v; want %v", id, got, want)
				}
			}
		})
	}
}

// A show folder gone from one library while one of the same name remains in
// another keeps its override IDs: manual_data.json can't tell them apart.
func TestMain_Clean_SameNameInTwoLibrariesKeepsOverrideIDs(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(t.TempDir(), "Show B")
	if err := os.Mkdir(kept, 0o755); err != nil {
		t.Fatal(err)
	}
	shows, _ := LoadCache(filepath.Join(cfg, "show_cache.json"))
	shows.Set(kept, "600")
	shows.Set(filepath.Join(t.TempDir(), "Show B"), "500") // gone
	mustSave(t, shows.Save())
	writeManualData(t, cfg, `{"Show B": {"overrides": {"S02": {"id": "550", "map_season": 1}}}}`)
	ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	for _, id := range []string{"500", "550", "600"} {
		ratings.SetShowRating(id, RatingPG)
	}
	mustSave(t, ratings.Save())

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

	if err != nil {
		t.Fatalf("--clean = %v:\n%s", err, out)
	}
	ratings, _ = LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	for id, want := range map[string]bool{"500": false, "550": true, "600": true} {
		if _, ok := ratings.GetShowRating(id); ok != want {
			t.Errorf("rating for show %s kept = %v; want %v", id, ok, want)
		}
	}
}

// With show_cache.json empty, --all-ids still keeps every show ID, even with
// override IDs known from manual_data.json.
func TestMain_Clean_AllIDsEmptyShowCacheWithOverrides(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManualData(t, cfg, `{"Show A": {"overrides": {"S02": {"id": "150", "map_season": 1}}}}`)
	ratings, _ := LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	ratings.SetShowRating("100", RatingPG)
	mustSave(t, ratings.Save())

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean", "--all-ids")

	if err != nil {
		t.Fatalf("--clean --all-ids = %v:\n%s", err, out)
	}
	ratings, _ = LoadRatingCache(filepath.Join(cfg, "rating_cache.json"))
	if _, ok := ratings.GetShowRating("100"); !ok {
		t.Errorf("show 100 lost its rating with show_cache.json empty:\n%s", out)
	}
}

// A manual_data.json that can't be loaded stops --clean before it removes
// anything.
func TestMain_Clean_CorruptManualDataExits(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := staleCaches(t, cfgHome)
	writeManualData(t, cfg, `{not json`)
	before, err := os.ReadFile(filepath.Join(cfg, "show_cache.json"))
	if err != nil {
		t.Fatal(err)
	}

	out, err := runMain(t, t.TempDir(), cfgHome, "--clean")

	if err == nil {
		t.Fatalf("--clean succeeded with a corrupt manual_data.json:\n%s", out)
	}
	if !strings.Contains(out, "Error loading manual_data.json") || !strings.Contains(out, "Nothing was removed.") {
		t.Errorf("output lacks the load error:\n%s", out)
	}
	after, _ := os.ReadFile(filepath.Join(cfg, "show_cache.json"))
	if string(after) != string(before) {
		t.Error("show_cache.json changed")
	}
}

func writeManualData(t *testing.T, cfg, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cfg, "manual_data.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
