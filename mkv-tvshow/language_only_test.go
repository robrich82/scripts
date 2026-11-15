package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// #172: every show with a folder gets a result line, in case-insensitive
// folder-name order whatever the map order, and the counts add up. A show
// whose language is already set costs no Plex lookup. A TMDB ID only the
// rating cache has (a season override's) is left out: it has no show in Plex.
func TestRunLanguageOnly_ResultLinesInNameOrder(t *testing.T) {
	rc := ratingCacheWithShows(t, filepath.Join(t.TempDir(), "rating_cache.json"), "1", "2", "3", "4", "5", "6", "7")
	rc.SetLanguageApplied("2", "ja-JP")
	cache := cacheWith(t, map[string]string{"Zeta": "1", "alpha": "2", "Mid": "3", "Beta": "4", "Gamma": "5", "Delta": "6"})
	var tmdbCalls []string
	tmdb := &langTMDB{
		fakeTMDB: &fakeTMDB{},
		langs:    map[string]string{"1": "ja", "2": "ja", "4": "ja", "6": "ja"},
		errs:     map[string]error{"5": errors.New("tmdb down")},
		onGet:    func(id string) { tmdbCalls = append(tmdbCalls, id) },
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
	if stats != (LanguageOnlyStats{Updated: 1, Skipped: 3, Errors: 2}) {
		t.Errorf("stats %+v; want 1 updated, 3 skipped, 2 errors", stats)
	}
	if slices.Contains(tmdbCalls, "7") {
		t.Error("TMDB was asked about 7, which has no show folder")
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
	plex := &fakePlexClient{setLangErr: fmt.Errorf("plex: prefs old: %w", ErrPlexNotFound)}
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

// #172: without Plex, --language-only exits 1 with the Plex error.
func TestMain_LanguageOnlyWithoutPlex_Exits(t *testing.T) {
	out, err := runMainEnv(t, t.TempDir(), t.TempDir(), []string{"TMDB_API_KEY=unused"}, "--language-only")
	if err == nil {
		t.Fatalf("--language-only without Plex succeeded:\n%s", out)
	}
	if got := strings.TrimSpace(out); got != "Error: --language-only sets the audio language in Plex: set PLEX_URL and PLEX_TOKEN." {
		t.Errorf("output %q; want only the Plex error", got)
	}
}

// #172: with Plex set and no cached shows, --language-only prints its header
// and a summary.
func TestMain_LanguageOnly_PrintsSummary(t *testing.T) {
	out, err := runMainEnv(t, t.TempDir(), t.TempDir(), []string{"TMDB_API_KEY=unused", "PLEX_URL=http://127.0.0.1:1", "PLEX_TOKEN=unused"}, "--language-only")
	if err != nil {
		t.Fatalf("--language-only failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Setting the Plex audio language for every cached show...") ||
		!strings.Contains(out, "Done. Updated: 0  Skipped: 0  Errors: 0") {
		t.Errorf("no header or summary:\n%s", out)
	}
}

// #172: a show cached under several folders (here an absolute path, its
// legacy bare-name key, and a second folder) gets one result line, named by
// the first folder. Each language key comes from the first folder whose
// manual_data.json entry sets it. A show only the show cache has is set too.
func TestRunLanguageOnly_SeveralFolders_MergesManualData(t *testing.T) {
	rc := emptyRatingCache(t)
	primary, alt, alt2 := &fakePlexClient{showRatingKey: "k1"}, &fakePlexClient{}, &fakePlexClient{}
	plexClient := newMultiPlexClient(primary, namedPlexClient{name: altAccount, client: alt}, namedPlexClient{name: alt2Account, client: alt2})
	tv := filepath.Join(t.TempDir(), "TV")
	cache := cacheWith(t, map[string]string{filepath.Join(tv, "show (2005)"): "1", filepath.Join(tv, "Show"): "1", "Show": "1"})
	manualData := map[string]ManualEntry{
		"Show":        {AltLanguage: "en"},
		"show (2005)": {Language: "ko", AltLanguage: "fr", Alt2Language: "de"},
	}
	p := NewProcessor(Config{}, cache, manualData, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, rc, nil, plexClient, nil)

	var stats LanguageOnlyStats
	out := captureStdout(t, func() { stats = p.RunLanguageOnly() })

	if got := strings.TrimSpace(out); got != "[UPDATED] Show (lang: ko-KR|alt=en-US|alt2=de-DE)" {
		t.Errorf("output %q; want one line for Show: ko and de from show (2005), en from Show", got)
	}
	if stats != (LanguageOnlyStats{Updated: 1}) {
		t.Errorf("stats %+v; want 1 updated", stats)
	}
	if len(primary.setLangCalls) != 1 || primary.setLangCalls[0].lang != "ko" || len(alt.setLangCalls) != 1 || alt.setLangCalls[0].lang != "en" ||
		len(alt2.setLangCalls) != 1 || alt2.setLangCalls[0].lang != "de" {
		t.Errorf("calls primary %+v, alt %+v, alt2 %+v; want ko, en and de", primary.setLangCalls, alt.setLangCalls, alt2.setLangCalls)
	}
}

// A show-cache entry with no TMDB ID is left out.
func TestRunLanguageOnly_EmptyID_Skipped(t *testing.T) {
	plex := &langPlex{fakePlexClient: &fakePlexClient{}}
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": ""}), nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, emptyRatingCache(t), nil, plex, nil)

	var stats LanguageOnlyStats
	out := captureStdout(t, func() { stats = p.RunLanguageOnly() })

	if out != "" || stats != (LanguageOnlyStats{}) || plex.lookups != 0 {
		t.Errorf("output %q, stats %+v, lookups %d; want nothing", out, stats, plex.lookups)
	}
}

// A title search that fails, after the GUID lookup found nothing, is an error.
func TestRunLanguageOnly_TitleSearchFails_IsError(t *testing.T) {
	plex := &fakePlexClient{findErr: ErrPlexNotFound, findTitleErr: errors.New("plex: HTTP 500")}
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "1"}), nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, emptyRatingCache(t), nil, plex, nil)

	var stats LanguageOnlyStats
	out := captureStdout(t, func() { stats = p.RunLanguageOnly() })

	if got := strings.TrimSpace(out); got != "[ERR]     Show A (failed: Plex lookup: plex: HTTP 500)" || stats != (LanguageOnlyStats{Errors: 1}) {
		t.Errorf("output %q, stats %+v; want one lookup error", got, stats)
	}
}

// The hint for a show with no known language names the folder to set it for.
func TestRunLanguageOnly_NoLanguage_HintNamesFirstFolder(t *testing.T) {
	cache := cacheWith(t, map[string]string{"Show B": "1", "Show A": "1"})
	p := NewProcessor(Config{}, cache, nil, &fakeTMDB{}, nil, nil, nil, nil, emptyRatingCache(t), nil, &fakePlexClient{}, nil)

	out := captureStdout(t, func() { p.RunLanguageOnly() })

	if got := strings.TrimSpace(out); got != `[SKIP]    Show A (TMDB has no original language; set "language" for "Show A" in manual_data.json)` {
		t.Errorf("output %q", got)
	}
}

// With alt accounts, a failed alt is a suffix on the show's line, not an
// error: UPDATED when the primary was pushed, SKIP when only the alt was due.
func TestRunLanguageOnly_AltFailure_ResultLines(t *testing.T) {
	rc := emptyRatingCache(t)
	cache := cacheWith(t, map[string]string{"Show A": "1"})
	run := func() (string, LanguageOnlyStats) {
		plexClient := newMultiPlexClient(&fakePlexClient{showRatingKey: "k1"}, namedPlexClient{name: altAccount, client: &fakePlexClient{setLangErr: errors.New("alt failed")}})
		p := NewProcessor(Config{}, cache, nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, rc, nil, plexClient, nil)
		var stats LanguageOnlyStats
		out := captureStdout(t, func() { stats = p.RunLanguageOnly() })
		return strings.TrimSpace(out), stats
	}

	out, stats := run()
	if out != "[UPDATED] Show A (lang: ja-JP|alt=ja-JP; alt: alt failed)" || stats != (LanguageOnlyStats{Updated: 1}) {
		t.Errorf("first run: %q, %+v; want UPDATED with the alt failure", out, stats)
	}
	out, stats = run()
	if out != "[SKIP]    Show A (lang: unchanged; alt: alt failed)" || stats != (LanguageOnlyStats{Skipped: 1}) {
		t.Errorf("retry: %q, %+v; want SKIP with the alt failure", out, stats)
	}
}

// A save that keeps failing is printed once, and the run ends by saying what
// was lost. A last save that fails is retried once after the run.
func TestRunLanguageOnly_FailedSave_RetriedAtEnd(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	for _, tc := range []struct {
		name     string
		recovers bool
		want     string
	}{
		{"recovers", true, "[i] Rating cache saved."},
		{"fails again", false, "rating_cache.json was never saved; this run's Plex keys and audio languages are lost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "missing")
			rc, err := LoadRatingCache(filepath.Join(dir, "rating_cache.json"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.recovers {
				setBeforeFinalSave(t, func() {
					if err := os.Mkdir(dir, 0o755); err != nil {
						t.Fatal(err)
					}
				})
			}
			plex := &langPlex{fakePlexClient: &fakePlexClient{}, byTMDB: map[string]string{"1": "k1", "2": "k2"}}
			tmdb := &langTMDB{fakeTMDB: &fakeTMDB{}, langs: map[string]string{"1": "ja", "2": "ko"}}
			p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "1", "Show B": "2"}), nil, tmdb, nil, nil, nil, nil, rc, nil, plex, nil)

			var stats LanguageOnlyStats
			out := captureOutput(t, func() { stats = p.RunLanguageOnly() })
			if want := map[bool]int{true: 0, false: 1}[tc.recovers]; stats.LostSaves != want {
				t.Errorf("LostSaves = %d; want %d", stats.LostSaves, want)
			}

			if n := strings.Count(out, "Failed to save rating cache"); n != 1 {
				t.Errorf("save failure printed %d times; want once:\n%s", n, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("no %q:\n%s", tc.want, out)
			}
			if !tc.recovers {
				return
			}
			saved, err := LoadRatingCache(filepath.Join(dir, "rating_cache.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := saved.GetLanguageApplied("2"); got != "ko-KR" {
				t.Errorf("Show B's language on disk = %q; want ko-KR", got)
			}
		})
	}
}

// When saves start failing partway, the warning names the last show saved.
func TestRunLanguageOnly_FailedSave_NamesLastSaved(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	path := filepath.Join(t.TempDir(), "rating_cache.json")
	rc := ratingCacheWithShows(t, path)
	tmdb := &langTMDB{
		fakeTMDB: &fakeTMDB{},
		langs:    map[string]string{"1": "ja", "2": "ko"},
		onGet: func(id string) {
			if id == "2" { // Show A is saved; a directory in the cache's place fails every later save
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	plex := &langPlex{fakePlexClient: &fakePlexClient{}, byTMDB: map[string]string{"1": "k1", "2": "k2"}}
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "1", "Show B": "2"}), nil, tmdb, nil, nil, nil, nil, rc, nil, plex, nil)

	out := captureOutput(t, func() { p.RunLanguageOnly() })

	if !strings.Contains(out, "[!] The rating cache "+path+" was last saved after Show A; later Plex keys and audio languages are lost") {
		t.Errorf("no warning naming Show A:\n%s", out)
	}
}

// A save that recovers mid-run says so, and a later failure is printed again.
func TestRunLanguageOnly_FailedSave_RecoversMidRun(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	dir := filepath.Join(t.TempDir(), "missing")
	rc, err := LoadRatingCache(filepath.Join(dir, "rating_cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	tmdb := &langTMDB{
		fakeTMDB: &fakeTMDB{},
		langs:    map[string]string{"1": "ja", "2": "ko", "3": "fr"},
		onGet: func(id string) {
			switch id {
			case "2": // Show A's save failed; Show B's succeeds
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "3": // Show C's fails again
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	plex := &langPlex{fakePlexClient: &fakePlexClient{}, byTMDB: map[string]string{"1": "k1", "2": "k2", "3": "k3"}}
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "1", "Show B": "2", "Show C": "3"}), nil, tmdb, nil, nil, nil, nil, rc, nil, plex, nil)

	out := captureOutput(t, func() { p.RunLanguageOnly() })

	if n := strings.Count(out, "Failed to save rating cache"); n != 2 {
		t.Errorf("save failure printed %d times; want twice:\n%s", n, out)
	}
	if !strings.Contains(out, "[UPDATED] Show B (lang: ko-KR)\n[i] Rating cache saved.\n") {
		t.Errorf("recovery after Show B not reported:\n%s", out)
	}
	if !strings.Contains(out, "last saved after Show B") {
		t.Errorf("end-of-run warning doesn't name Show B:\n%s", out)
	}
}

// setBeforeFinalSave replaces cacheSaver.finish's pre-retry pause for one test.
func setBeforeFinalSave(t *testing.T, f func()) {
	old := beforeFinalSave
	beforeFinalSave = f
	t.Cleanup(func() { beforeFinalSave = old })
}
