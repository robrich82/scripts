package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// langPlex answers show lookups per TMDB ID and title, and counts them.
type langPlex struct {
	*fakePlexClient
	byTMDB  map[string]string // missing: ErrPlexNotFound
	tmdbErr map[string]error
	byTitle map[string]string // missing: ErrPlexNotFound
	lookups int
}

func (f *langPlex) FindShowByTMDBID(id string) (string, error) {
	f.lookups++
	if err := f.tmdbErr[id]; err != nil {
		return "", err
	}
	if k, ok := f.byTMDB[id]; ok {
		return k, nil
	}
	return "", ErrPlexNotFound
}

func (f *langPlex) FindShowByTitle(title string) (string, error) {
	f.lookups++
	if k, ok := f.byTitle[title]; ok {
		return k, nil
	}
	return "", ErrPlexNotFound
}

// langTMDB answers GetShow per TMDB ID.
type langTMDB struct {
	*fakeTMDB
	langs map[string]string
	errs  map[string]error
	onGet func(id string)
}

func (f *langTMDB) GetShow(id string) (ShowDetails, error) {
	if f.onGet != nil {
		f.onGet(id)
	}
	if err := f.errs[id]; err != nil {
		return ShowDetails{}, err
	}
	return ShowDetails{OriginalLanguage: f.langs[id]}, nil
}

func ratingCacheWithShows(t *testing.T, path string, ids ...string) *RatingCache {
	t.Helper()
	rc, err := LoadRatingCache(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		rc.SetShowRating(id, RatingM)
	}
	return rc
}

// #172: every show gets a result line, in folder-name order whatever the map
// order, and the counts add up. A show whose language is already set costs no
// Plex lookup.
func TestRunLanguageOnly_ResultLinesInNameOrder(t *testing.T) {
	rc := ratingCacheWithShows(t, filepath.Join(t.TempDir(), "rating_cache.json"), "1", "2", "3", "4", "5", "6", "7")
	rc.SetLanguageApplied("2", "ja-JP")
	cache := cacheWith(t, map[string]string{"Zeta": "1", "alpha": "2", "Mid": "3", "Beta": "4", "Gamma": "5", "Delta": "6"})
	tmdb := &langTMDB{
		fakeTMDB: &fakeTMDB{},
		langs:    map[string]string{"1": "ja", "2": "ja", "4": "ja", "6": "ja"},
		errs:     map[string]error{"5": errors.New("tmdb down")},
	}
	plex := &langPlex{
		fakePlexClient: &fakePlexClient{},
		byTMDB:         map[string]string{"1": "k1"},
		tmdbErr:        map[string]error{"6": errors.New("plex: HTTP 500")},
	}
	p := NewProcessor(Config{}, cache, nil, tmdb, nil, nil, nil, nil, rc, nil, plex, nil)

	var stats LanguageOnlyStats
	out := captureStdout(t, func() { stats = p.RunLanguageOnly() })

	want := []string{
		`[SKIP]    TMDB 7 (TMDB has no original language, and no show folder is cached to set "language" for)`,
		`[SKIP]    alpha (language set earlier)`,
		`[SKIP]    Beta (not in Plex; rerun with --language-only once Plex has it)`,
		`[ERR]     Delta (failed: Plex lookup: plex: HTTP 500)`,
		`[ERR]     Gamma: TMDB show: tmdb down`,
		`[SKIP]    Mid (TMDB has no original language; set "language" for "Mid" in manual_data.json)`,
		`[UPDATED] Zeta (lang: ja-JP)`,
	}
	if got := strings.Split(strings.TrimSpace(out), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("output:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if stats != (LanguageOnlyStats{Updated: 1, Skipped: 4, Errors: 2}) {
		t.Errorf("stats %+v; want 1 updated, 4 skipped, 2 errors", stats)
	}
	// Beta: GUID then title; Delta: GUID only (an error isn't "not found"); Zeta: GUID.
	if plex.lookups != 4 {
		t.Errorf("Plex lookups %d; want 4 (none for alpha, already set)", plex.lookups)
	}
	if len(plex.setLangCalls) != 1 || plex.setLangCalls[0].ratingKey != "k1" {
		t.Errorf("SetAudioLanguage calls %+v; want one, for k1", plex.setLangCalls)
	}
	if key, _ := rc.GetPlexKey("1"); key != "k1" {
		t.Errorf("Zeta's Plex key %q not cached", key)
	}
}

// #172: the rating cache is saved after each show, so an interrupted run keeps
// what it already set.
func TestRunLanguageOnly_SavesAfterEachShow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rating_cache.json")
	rc := ratingCacheWithShows(t, path, "1", "2")
	var onDisk string
	tmdb := &langTMDB{
		fakeTMDB: &fakeTMDB{},
		langs:    map[string]string{"1": "ja", "2": "ko"},
		onGet: func(id string) {
			if id == "2" { // Show B comes second: Show A must be on disk by now
				saved, err := LoadRatingCache(path)
				if err != nil {
					t.Fatal(err)
				}
				onDisk, _ = saved.GetLanguageApplied("1")
			}
		},
	}
	plex := &langPlex{fakePlexClient: &fakePlexClient{}, byTMDB: map[string]string{"1": "k1", "2": "k2"}}
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "1", "Show B": "2"}), nil, tmdb, nil, nil, nil, nil, rc, nil, plex, nil)

	captureStdout(t, func() { p.RunLanguageOnly() })

	if onDisk != "ja-JP" {
		t.Errorf("Show A's language on disk before Show B ran = %q; want ja-JP", onDisk)
	}
	saved, err := LoadRatingCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := saved.GetLanguageApplied("2"); got != "ko-KR" {
		t.Errorf("Show B's language on disk = %q; want ko-KR", got)
	}
}

// #172: a wrapped not-found, or an empty key, from the GUID lookup falls back
// to the folder name's title search.
func TestRunLanguageOnly_GUIDNotFound_FallsBackToTitle(t *testing.T) {
	for name, guidErr := range map[string]error{
		"wrapped not found": fmt.Errorf("plex: show 1: %w", ErrPlexNotFound),
		"empty key":         nil,
	} {
		t.Run(name, func(t *testing.T) {
			rc := ratingCacheWithShows(t, filepath.Join(t.TempDir(), "rating_cache.json"), "1")
			plex := &langPlex{fakePlexClient: &fakePlexClient{}, byTMDB: map[string]string{}, byTitle: map[string]string{"Show A": "k9"}}
			if guidErr != nil {
				plex.tmdbErr = map[string]error{"1": guidErr}
			} else {
				plex.byTMDB["1"] = ""
			}
			tmdb := &langTMDB{fakeTMDB: &fakeTMDB{}, langs: map[string]string{"1": "ja"}}
			p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A (2020)": "1"}), nil, tmdb, nil, nil, nil, nil, rc, nil, plex, nil)

			out := captureStdout(t, func() { p.RunLanguageOnly() })

			if len(plex.setLangCalls) != 1 || plex.setLangCalls[0].ratingKey != "k9" {
				t.Errorf("SetAudioLanguage calls %+v; want one, for the title match k9:\n%s", plex.setLangCalls, out)
			}
		})
	}
}

// A cached show key Plex no longer has is cleared and reported, so the next
// run looks the show up again.
func TestRunLanguageOnly_StaleShowKey_ClearedAndReported(t *testing.T) {
	rc := ratingCacheWithShows(t, filepath.Join(t.TempDir(), "rating_cache.json"), "1")
	rc.SetPlexKey("1", "old")
	plex := &fakePlexClient{setLangErr: ErrPlexNotFound}
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "1"}), nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, rc, nil, plex, nil)

	var stats LanguageOnlyStats
	out := captureStdout(t, func() { stats = p.RunLanguageOnly() })

	if !strings.Contains(out, "[SKIP]    Show A (Plex no longer has show old; key cleared, rerun with --language-only to look it up again)") {
		t.Errorf("stale key not reported:\n%s", out)
	}
	if key, ok := rc.GetPlexKey("1"); ok && key != "" {
		t.Errorf("stale key %q kept", key)
	}
	if stats.Skipped != 1 || stats.Errors != 0 {
		t.Errorf("stats %+v; want 1 skipped", stats)
	}
}

// A failed language push is an error, and nothing is recorded as applied.
func TestRunLanguageOnly_SetFails_IsError(t *testing.T) {
	rc := ratingCacheWithShows(t, filepath.Join(t.TempDir(), "rating_cache.json"), "1")
	plex := &fakePlexClient{showRatingKey: "k1", setLangErr: errors.New("plex: HTTP 500")}
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "1"}), nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, rc, nil, plex, nil)

	var stats LanguageOnlyStats
	out := captureStdout(t, func() { stats = p.RunLanguageOnly() })

	if !strings.Contains(out, "[ERR]     Show A (failed: Plex language: plex: HTTP 500)") || stats.Errors != 1 {
		t.Errorf("stats %+v; want the failure reported as an error:\n%s", stats, out)
	}
	if _, ok := rc.GetLanguageApplied("1"); ok {
		t.Error("language recorded after a failed push")
	}
}

// #172: without Plex, --language-only exits 1 instead of printing to stderr
// and exiting 0.
func TestMain_LanguageOnlyWithoutPlex_Exits(t *testing.T) {
	out, err := runMainEnv(t, t.TempDir(), t.TempDir(), []string{"TMDB_API_KEY=unused"}, "--language-only")
	if err == nil {
		t.Fatalf("--language-only without Plex succeeded:\n%s", out)
	}
	if got := strings.TrimSpace(out); got != "Error: --language-only sets the audio language in Plex: set PLEX_URL and PLEX_TOKEN." {
		t.Errorf("output %q; want only the Plex error", got)
	}
}
