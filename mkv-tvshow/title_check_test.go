package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStripPartSuffix(t *testing.T) {
	cases := []struct {
		input      string
		wantBase   string
		wantSuffix string
	}{
		{"Some Title-part1", "Some Title", "-part1"},
		{"Some Title-part2", "Some Title", "-part2"},
		{"Some Title-Part1", "Some Title", "-Part1"},
		{"Some Title", "Some Title", ""},
		{"Title-partial", "Title-partial", ""}, // word "partial" must not match
		{"Title-part10", "Title", "-part10"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			base, suffix := stripPartSuffix(tc.input)
			if base != tc.wantBase || suffix != tc.wantSuffix {
				t.Errorf("stripPartSuffix(%q) = (%q, %q), want (%q, %q)",
					tc.input, base, suffix, tc.wantBase, tc.wantSuffix)
			}
		})
	}
}

func TestExtractFileTitles_PartSuffix(t *testing.T) {
	cases := []struct {
		stem string
		want []string
	}{
		// Single episode, part suffix stripped before comparison.
		{"Show - S01E05 - Some Episode-part1", []string{"Some Episode"}},
		{"Show - S01E05 - Some Episode-part2", []string{"Some Episode"}},
		{"Show - S01E05 - Some Episode-Part1", []string{"Some Episode"}},
		// Multi-episode: suffix on the last title only.
		{"Show - S01E05E06 - Title A﹢Title B-part1", []string{"Title A", "Title B"}},
		// No suffix: unchanged.
		{"Show - S01E05 - Some Episode", []string{"Some Episode"}},
	}
	for _, tc := range cases {
		t.Run(tc.stem, func(t *testing.T) {
			got := extractFileTitles(tc.stem)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractFileTitles(%q) = %v, want %v", tc.stem, got, tc.want)
			}
		})
	}
}

func TestBuildNewStemFromFS_PartSuffix(t *testing.T) {
	cases := []struct {
		oldStem     string
		fsTitlePart string
		wantStem    string
	}{
		{
			"Show - S01E05 - Old Title-part1",
			"New Title",
			"Show - S01E05 - New Title-part1",
		},
		{
			"Show - S01E05 - Old Title-part2",
			"New Title",
			"Show - S01E05 - New Title-part2",
		},
		{
			"Show - S01E05 - Old Title",
			"New Title",
			"Show - S01E05 - New Title",
		},
	}
	for _, tc := range cases {
		t.Run(tc.oldStem, func(t *testing.T) {
			got, _ := buildNewStemFromFS(tc.oldStem, tc.fsTitlePart, `C:\Media\Show`)
			if got != tc.wantStem {
				t.Errorf("buildNewStemFromFS(%q, %q) = %q, want %q",
					tc.oldStem, tc.fsTitlePart, got, tc.wantStem)
			}
		})
	}
}

func TestAllPlaceholders(t *testing.T) {
	cases := []struct {
		name   string
		titles []string
		want   bool
	}{
		{"empty", []string{}, false},
		{"single placeholder", []string{"Episode 2"}, true},
		{"placeholder case insensitive", []string{"episode 12"}, true},
		{"real title", []string{"Ami Has Arrived"}, false},
		{"mixed", []string{"Episode 1", "Ami Has Arrived"}, false},
		{"multi all placeholders", []string{"Episode 1", "Episode 2"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allPlaceholders(tc.titles)
			if got != tc.want {
				t.Errorf("allPlaceholders(%v) = %v, want %v", tc.titles, got, tc.want)
			}
		})
	}
}

// noopTimer satisfies FileTimer without touching the filesystem.
type noopTimer struct{}

func (noopTimer) SetTimes(_ string, _ time.Time) error        { return nil }
func (noopTimer) SetCreationTime(_ string, _ time.Time) error { return nil }

// newNullTitleCache returns a TitleCache backed by a temp file.
func newNullTitleCache(t *testing.T) *TitleCache {
	t.Helper()
	tc, _ := LoadTitleCache(filepath.Join(t.TempDir(), "title_cache.json"))
	return tc
}

// assertNoAskPrompter fails the test if Ask is ever called.
type assertNoAskPrompter struct{ t *testing.T }

func (p *assertNoAskPrompter) Ask(q string) (string, error) {
	p.t.Errorf("unexpected rename prompt when TMDB has placeholder title: %q", q)
	return "n", nil
}

// TestTitleCheck_SkipsPlaceholderRename verifies that when TMDB returns a
// placeholder title ("Episode N") but the file already has a real title, no
// rename is proposed and the file is left untouched.
func TestTitleCheck_SkipsPlaceholderRename(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Show")
	if err := os.Mkdir(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filename := "Show - S01E02 - Ami Has Arrived.mkv"
	mkvPath := filepath.Join(showDir, filename)
	if err := os.WriteFile(mkvPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"Show": "tt123"})
	tmdbClient := &fakeTMDB{
		seasonResult: SeasonResult{Episodes: []Episode{
			{EpisodeNumber: 2, Name: "Episode 2"},
		}},
	}

	tc := NewTitleChecker(
		Config{MediaDir: dir},
		cache,
		nil,
		tmdbClient,
		&assertNoAskPrompter{t: t},
		nil, // no mkvpropedit
		newNullTitleCache(t),
		noopTimer{},
	)

	if err := tc.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// File must not have been renamed.
	if _, err := os.Stat(mkvPath); os.IsNotExist(err) {
		t.Errorf("file was renamed or deleted: %s", mkvPath)
	}
}
