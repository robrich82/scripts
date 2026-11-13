package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// moviesTMDB serves each movie's details by TMDB ID, counting the calls.
type moviesTMDB struct {
	details map[string]MovieDetails
	errs    map[string]error
	fetches int
}

func (m *moviesTMDB) SearchAndFetch(_, _ string) (string, MovieDetails, error) {
	return "", MovieDetails{}, errors.New("--language-only must not search TMDB")
}

func (m *moviesTMDB) GetMovieDetails(id string) (MovieDetails, error) {
	m.fetches++
	if err := m.errs[id]; err != nil {
		return MovieDetails{}, err
	}
	return m.details[id], nil
}

// langPlex finds movies like fakePlexMovieClient and records the language of
// each SetMovieStreams call, reporting a change when changed is set.
type langPlex struct {
	fakePlexMovieClient
	changed   bool
	streamErr error
	langs     []string
}

func (p *langPlex) SetMovieStreams(_ string, langs AudioLangs) (bool, bool, error) {
	p.langs = append(p.langs, langs.Default)
	if p.streamErr != nil {
		return false, false, p.streamErr
	}
	return p.changed, false, nil
}

var (
	cocoDetails     = MovieDetails{TMDBID: "354912", Title: "Coco", ReleaseDate: "2017-10-27", OriginalLanguage: "en"}
	spiritedDetails = MovieDetails{TMDBID: "129", Title: "Spirited Away", ReleaseDate: "2001-07-20", OriginalLanguage: "ja"}
)

// languageCache is a cache mapping files, which don't exist, to movies.
func languageCache(t *testing.T, files map[string]string) *MovieRatingCache {
	t.Helper()
	cache := makeCache(t)
	dir := t.TempDir()
	for name, id := range files {
		cache.SetFileTMDBID(filepath.Join(dir, "missing", name), id)
	}
	return cache
}

func languageOnlyRun(t *testing.T, tmdb TMDBMovieClient, plex PlexMovieClient, cache *MovieRatingCache, manualData map[string]MovieManualEntry, overwrite bool) (Stats, string) {
	t.Helper()
	var stats Stats
	out := captureStdout(t, func() {
		stats = walkLanguageOnly(tmdb, plex, cache, manualData, overwrite)
	})
	return stats, out
}

func reloaded(t *testing.T, cache *MovieRatingCache) *MovieRatingCache {
	t.Helper()
	saved, err := LoadMovieRatingCache(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// #134: every cached movie gets its streams, in name order, without its file
// being read; the record is saved, so a second run sets nothing.
func TestLanguageOnly_SetsEveryCachedMovie(t *testing.T) {
	cache := languageCache(t, map[string]string{"Spirited Away (2001).mkv": "129", "Coco (2017).mkv": "354912"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"129": spiritedDetails, "354912": cocoDetails}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}, changed: true}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats.Updated != 2 || stats.Skipped != 0 || stats.Errors != 0 {
		t.Errorf("stats %+v; want 2 updated", stats)
	}
	want := "[UPDATED] Coco (2017) (lang: en)\n[UPDATED] Spirited Away (2001) (lang: ja)\n"
	if !strings.Contains(out, want) {
		t.Errorf("output isn't %q:\n%s", want, out)
	}
	if tmdb.fetches != 2 {
		t.Errorf("TMDB details calls = %d; want one per movie", tmdb.fetches)
	}
	saved := reloaded(t, cache)
	if got, _ := saved.GetStreamsApplied("129"); got != "ja" {
		t.Errorf("saved record for 129 = %q; want ja", got)
	}

	plex.langs = nil
	stats, out = languageOnlyRun(t, tmdb, plex, saved, nil, false)
	if stats.Skipped != 2 || len(plex.langs) != 0 {
		t.Errorf("second run: stats %+v, stream sets %v; want both skipped, nothing set", stats, plex.langs)
	}
	if !strings.Contains(out, "[SKIP]    Coco (2017) (streams set earlier)") {
		t.Errorf("second run output:\n%s", out)
	}
}

// --overwrite-streams sets a movie's streams again; Plex already having them
// is a skip, not an update.
func TestLanguageOnly_OverwriteStreams(t *testing.T) {
	cache := languageCache(t, map[string]string{"Coco (2017).mkv": "354912"})
	cache.SetStreamsApplied("354912", "en")
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"354912": cocoDetails}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, true)

	if !slices.Equal(plex.langs, []string{"en"}) {
		t.Errorf("stream sets %v; want [en]", plex.langs)
	}
	if stats.Skipped != 1 || !strings.Contains(out, "[SKIP]    Coco (2017) (lang: unchanged)") {
		t.Errorf("stats %+v:\n%s", stats, out)
	}
}

// A movie's languages come from the manual_data.json entry of any of its
// files. With "language" set, TMDB is called only once the streams are due.
func TestLanguageOnly_ManualDataLanguage(t *testing.T) {
	cache := languageCache(t, map[string]string{"Spirited Away (2001).mkv": "129", "Spirited Away (2001) Remastered.mkv": "129"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"129": spiritedDetails}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}, changed: true}
	manual := map[string]MovieManualEntry{"Spirited Away (2001) Remastered": {Language: "en"}}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, manual, false)

	if stats.Updated != 1 || !slices.Equal(plex.langs, []string{"en"}) {
		t.Errorf("stats %+v, stream sets %v; want one movie set to en", stats, plex.langs)
	}
	if !strings.Contains(out, "[UPDATED] Spirited Away (2001) (lang: en)") {
		t.Errorf("output:\n%s", out)
	}

	tmdb.fetches = 0
	stats, _ = languageOnlyRun(t, tmdb, plex, reloaded(t, cache), manual, false)
	if stats.Skipped != 1 || tmdb.fetches != 0 {
		t.Errorf("second run: stats %+v, TMDB calls %d; want a skip with no TMDB call", stats, tmdb.fetches)
	}
}

// A movie Plex doesn't have is searched by TMDB's title and year, not the
// file's; not finding it is a warning, and nothing is recorded.
func TestLanguageOnly_NotInPlex(t *testing.T) {
	cache := languageCache(t, map[string]string{"Thing 1982.mkv": "1091"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"1091": {Title: "The Thing", ReleaseDate: "1982-06-25", OriginalLanguage: "en"}}}
	plex := &langPlex{}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats.Skipped != 1 || stats.Errors != 0 {
		t.Errorf("stats %+v; want 1 skipped", stats)
	}
	if !slices.Equal(plex.titleLookups, [][2]string{{"The Thing", "1982"}}) {
		t.Errorf("title lookups %v; want TMDB's title and year", plex.titleLookups)
	}
	for _, want := range []string{`Plex: "The Thing" not found`, "rerun with --language-only", "[SKIP]    Thing 1982 (not in Plex)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, ok := reloaded(t, cache).GetStreamsApplied("1091"); ok || len(plex.langs) != 0 {
		t.Error("streams set or recorded for a movie Plex doesn't have")
	}
}

// Failures are errors that don't stop the run, and leave nothing recorded.
func TestLanguageOnly_Failures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tmdbErr  error
		plex     *langPlex
		wantLine string
		wantHint string
	}{
		{"TMDB details", errors.New("timeout"), &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}, "[ERR]     Coco (2017): TMDB details: timeout\n", ""},
		{"Plex lookup", nil, &langPlex{fakePlexMovieClient: fakePlexMovieClient{findTMDBErr: errors.New("status 500")}}, "[ERR]     Coco (2017) (failed: Plex lookup)\n", "Plex lookup failed – streams not set: status 500"},
		{"no audio track", nil, &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}, streamErr: fmt.Errorf("%w: en, key 10", ErrPlexNoAudioStream)}, "[ERR]     Coco (2017) (failed: Plex streams)\n", `Set "language" for "Coco (2017)" in manual_data.json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := languageCache(t, map[string]string{"Coco (2017).mkv": "354912", "Spirited Away (2001).mkv": "129"})
			tmdb := &moviesTMDB{details: map[string]MovieDetails{"354912": cocoDetails, "129": spiritedDetails}, errs: map[string]error{"354912": tc.tmdbErr}}
			if tc.tmdbErr == nil {
				tmdb.errs = nil
			}

			stats, out := languageOnlyRun(t, tmdb, tc.plex, cache, nil, false)

			if stats.Errors < 1 || !strings.Contains(out, tc.wantLine) {
				t.Errorf("stats %+v; output lacks %q:\n%s", stats, tc.wantLine, out)
			}
			if tc.wantHint != "" && !strings.Contains(out, tc.wantHint) {
				t.Errorf("output lacks %q:\n%s", tc.wantHint, out)
			}
			if _, ok := reloaded(t, cache).GetStreamsApplied("354912"); ok {
				t.Error("failed movie recorded")
			}
			if !strings.Contains(out, "Spirited Away (2001)") {
				t.Errorf("run stopped at the failure:\n%s", out)
			}
		})
	}
}

// With no language from manual_data.json or TMDB, nothing is set.
func TestLanguageOnly_NoLanguage(t *testing.T) {
	cache := languageCache(t, map[string]string{"Coco (2017).mkv": "354912"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"354912": {Title: "Coco"}}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats.Skipped != 1 || len(plex.langs) != 0 {
		t.Errorf("stats %+v, stream sets %v; want a skip", stats, plex.langs)
	}
	if !strings.Contains(out, `[SKIP]    Coco (2017) (TMDB has no original language; set "language" for "Coco (2017)" in manual_data.json)`) {
		t.Errorf("output:\n%s", out)
	}
}

// #141 in --language-only: a failed alt is a warning shown on the line and
// left out of the record; the next run sets that alt alone.
func TestLanguageOnly_AltAccounts_RetryOnlyFailed(t *testing.T) {
	cache := languageCache(t, map[string]string{"Coco (2017).mkv": "354912"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"354912": cocoDetails}}
	primary := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}
	plex := newMultiPlexMovieClient(primary, namedPlexMovieClient{name: altAccount, client: failingAlt()})

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats.Skipped != 1 || !strings.Contains(out, "[SKIP]    Coco (2017) (lang: unchanged; alt: alt failed)") {
		t.Errorf("stats %+v:\n%s", stats, out)
	}
	saved := reloaded(t, cache)
	if got, _ := saved.GetStreamsApplied("354912"); got != "en" {
		t.Fatalf("recorded %q; want en, without the failed alt", got)
	}

	alt := &fakeMultiPlexMovieClient{}
	primary.langs = nil
	plex = newMultiPlexMovieClient(primary, namedPlexMovieClient{name: altAccount, client: alt})
	stats, out = languageOnlyRun(t, tmdb, plex, saved, nil, false)

	if len(primary.langs) != 0 || alt.setStreamsCalls != 1 {
		t.Errorf("retry: primary sets %v, alt calls %d; want only the alt set", primary.langs, alt.setStreamsCalls)
	}
	if stats.Updated != 1 || !strings.Contains(out, "[UPDATED] Coco (2017) (lang: en|alt=en)") {
		t.Errorf("retry stats %+v:\n%s", stats, out)
	}
	if got, _ := reloaded(t, saved).GetStreamsApplied("354912"); got != "en|alt=en" {
		t.Errorf("recorded %q; want en|alt=en", got)
	}
}

// Integration: the real Plex client against a server. The movie is found by
// its TMDB GUID, its Japanese audio and English subtitle are selected, and the
// record reaches the cache file.
func TestLanguageOnly_HTTPPlex(t *testing.T) {
	var mu sync.Mutex
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut:
			mu.Lock()
			puts = append(puts, r.URL.Path+"?"+r.URL.RawQuery)
			mu.Unlock()
		case r.URL.Path == "/library/sections":
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		case strings.HasPrefix(r.URL.Path, "/library/sections/2/"):
			fmt.Fprint(w, plexAllMoviesJSON("77", "tmdb://129"))
		case r.URL.Path == "/library/metadata/77":
			fmt.Fprint(w, streamsMetaJSON("ja-JP", false, false)) // Plex tags streams with a region
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cache := languageCache(t, map[string]string{"Spirited Away (2001).mkv": "129"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"129": spiritedDetails}}

	stats, out := languageOnlyRun(t, tmdb, newHTTPPlexMovieClient(srv.URL, "tok"), cache, nil, false)

	if stats.Updated != 1 || !strings.Contains(out, "[UPDATED] Spirited Away (2001) (lang: ja)") {
		t.Errorf("stats %+v:\n%s", stats, out)
	}
	want := []string{"/library/parts/100?audioStreamID=201", "/library/parts/100?subtitleStreamID=301"}
	if len(puts) != 2 || !strings.HasPrefix(puts[0], want[0]) || !strings.HasPrefix(puts[1], want[1]) {
		t.Errorf("PUTs %v; want %v", puts, want)
	}
	if got, _ := reloaded(t, cache).GetStreamsApplied("129"); got != "ja" {
		t.Errorf("saved record = %q; want ja", got)
	}
}
