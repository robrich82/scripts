package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// --- fakePlexClient for use in processor tests ---

type setRatingCall struct {
	ratingKey string
	itemType  PlexItemType
	rating    NZRating
}

type setLangCall struct {
	ratingKey string
	lang      string
}

type setEpMetaCall struct {
	ratingKey             string
	title                 string
	originallyAvailableAt string
}

type fakePlexClient struct {
	showRatingKey     string
	findErr           error
	titleRatingKey    string
	findTitleErr      error
	filePathRatingKey string
	filePathErr       error
	seasons           []PlexSeason
	seasonsErr        error
	episodesByKey     map[string][]PlexEpisodeItem
	episodesErr       error
	setRatingCalls    []setRatingCall
	setRatingErr      error
	contentRating     NZRating
	contentRatingErr  error
	setLangCalls      []setLangCall
	setLangErr        error
	setEpMetaCalls    []setEpMetaCall
	setEpMetaErr      error
}

func (f *fakePlexClient) FindShowByTMDBID(_ string) (string, error) {
	return f.showRatingKey, f.findErr
}

func (f *fakePlexClient) FindShowByTitle(_ string) (string, error) {
	return f.titleRatingKey, f.findTitleErr
}

func (f *fakePlexClient) FindShowByFilePath(_ string) (string, error) {
	return f.filePathRatingKey, f.filePathErr
}

func (f *fakePlexClient) GetSeasons(_ string) ([]PlexSeason, error) {
	return f.seasons, f.seasonsErr
}

func (f *fakePlexClient) GetEpisodes(seasonRatingKey string) ([]PlexEpisodeItem, error) {
	if f.episodesErr != nil {
		return nil, f.episodesErr
	}
	if f.episodesByKey != nil {
		return f.episodesByKey[seasonRatingKey], nil
	}
	return nil, nil
}

func (f *fakePlexClient) GetContentRating(_ string) (NZRating, error) {
	return f.contentRating, f.contentRatingErr
}

func (f *fakePlexClient) SetContentRating(ratingKey string, t PlexItemType, rating NZRating) error {
	f.setRatingCalls = append(f.setRatingCalls, setRatingCall{ratingKey, t, rating})
	return f.setRatingErr
}

// SetAudioLanguage records the language this account would actually apply,
// i.e. langs.Default — multiPlexClient resolves each alt's override before
// calling its client, so Default is that account's own language.
func (f *fakePlexClient) SetAudioLanguage(ratingKey string, langs AudioLangs) error {
	f.setLangCalls = append(f.setLangCalls, setLangCall{ratingKey, langs.Default})
	return f.setLangErr
}

func (f *fakePlexClient) SetEpisodeMetadata(ratingKey, title, originallyAvailableAt string) error {
	f.setEpMetaCalls = append(f.setEpMetaCalls, setEpMetaCall{ratingKey, title, originallyAvailableAt})
	return f.setEpMetaErr
}

func (f *fakePlexClient) RefreshLibraries() error        { return nil }
func (f *fakePlexClient) RefreshMetadata(_ string) error { return nil }

// --- httpPlexClient unit tests ---

func plexSectionsJSON(sectionKey, sectionType string) string {
	return `{"MediaContainer":{"Directory":[{"key":"` + sectionKey + `","type":"` + sectionType + `"}]}}`
}

func plexAllShowsJSON(ratingKey, tmdbGUID string) string {
	return `{"MediaContainer":{"Metadata":[{"ratingKey":"` + ratingKey + `","Guid":[{"id":"` + tmdbGUID + `"}]}]}}`
}

// plexAllShowsJSONLegacyGuid returns JSON where Guid is a plain string (not an array),
// matching the format returned by older Plex metadata agents.
func plexAllShowsJSONLegacyGuid(ratingKey, guidStr string) string {
	return `{"MediaContainer":{"Metadata":[{"ratingKey":"` + ratingKey + `","Guid":"` + guidStr + `"}]}}`
}

// plexAllShowsJSONLegacyLower returns JSON where the lowercase guid field holds
// the legacy agent string (e.g. "com.plexapp.agents.themoviedb://12345?lang=en").
func plexAllShowsJSONLegacyLower(ratingKey, guidStr string) string {
	return `{"MediaContainer":{"Metadata":[{"ratingKey":"` + ratingKey + `","guid":"` + guidStr + `"}]}}`
}

// Like Plex, the fake server lists a modern-agent show's Guid array only when
// asked with includeGuids=1; otherwise the show has just its plex:// guid.
func TestHTTPPlexClient_FindShowByTMDBID_Found(t *testing.T) {
	var gotSectionPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/library/sections":
			fmt.Fprint(w, plexSectionsJSON("1", "show"))
		case r.URL.Query().Get("includeGuids") == "1":
			gotSectionPath = r.URL.Path
			fmt.Fprint(w, plexAllShowsJSON("42", "tmdb://12345"))
		default:
			gotSectionPath = r.URL.Path
			fmt.Fprint(w, plexAllShowsJSONLegacyLower("42", "plex://show/5d9c086c46115600200aa2fe"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	key, err := c.FindShowByTMDBID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "42" {
		t.Errorf("got ratingKey %q; want 42", key)
	}
	if gotSectionPath != "/library/sections/1/all" {
		t.Errorf("unexpected section path %q", gotSectionPath)
	}
}

func TestHTTPPlexClient_FindShowByTMDBID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "show"))
		} else {
			// Show present but different TMDB ID.
			fmt.Fprint(w, plexAllShowsJSON("99", "tmdb://99999"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	_, err := c.FindShowByTMDBID("12345")
	if err != ErrPlexNotFound {
		t.Errorf("expected ErrPlexNotFound, got %v", err)
	}
}

func TestHTTPPlexClient_FindShowByTMDBID_SkipsMovieSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			// Only a "movie" section — should be skipped.
			fmt.Fprint(w, plexSectionsJSON("1", "movie"))
		} else {
			t.Error("should not scan non-show section")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	_, err := c.FindShowByTMDBID("12345")
	if err != ErrPlexNotFound {
		t.Errorf("expected ErrPlexNotFound, got %v", err)
	}
}

func TestHTTPPlexClient_FindShowByTMDBID_CachesResult(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "show"))
		} else {
			fmt.Fprint(w, plexAllShowsJSON("42", "tmdb://12345"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	if _, err := c.FindShowByTMDBID("12345"); err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := calls
	if _, err := c.FindShowByTMDBID("12345"); err != nil { // should use cache
		t.Fatal(err)
	}
	if calls != callsAfterFirst {
		t.Errorf("second FindShowByTMDBID made HTTP requests (calls went from %d to %d)", callsAfterFirst, calls)
	}
}

func TestHTTPPlexClient_GetSeasons(t *testing.T) {
	body := `{"MediaContainer":{"Metadata":[
		{"ratingKey":"10","type":"season","index":1},
		{"ratingKey":"11","type":"season","index":2},
		{"ratingKey":"12","type":"episode","index":1}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	seasons, err := c.GetSeasons("5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seasons) != 2 {
		t.Fatalf("got %d seasons; want 2", len(seasons))
	}
	if seasons[0].RatingKey != "10" || seasons[0].SeasonNumber != 1 {
		t.Errorf("season[0] = %+v; want {10, 1}", seasons[0])
	}
	if seasons[1].RatingKey != "11" || seasons[1].SeasonNumber != 2 {
		t.Errorf("season[1] = %+v; want {11, 2}", seasons[1])
	}
}

func TestHTTPPlexClient_GetEpisodes(t *testing.T) {
	body := `{"MediaContainer":{"Metadata":[
		{"ratingKey":"20","type":"episode","parentIndex":1,"index":1},
		{"ratingKey":"21","type":"episode","parentIndex":1,"index":2},
		{"ratingKey":"22","type":"season","parentIndex":0,"index":1}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	eps, err := c.GetEpisodes("10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("got %d episodes; want 2", len(eps))
	}
	if eps[0].RatingKey != "20" || eps[0].EpisodeNumber != 1 {
		t.Errorf("ep[0] = %+v; want {20, 1, 1}", eps[0])
	}
}

func TestHTTPPlexClient_SetContentRating_SendsCorrectParams(t *testing.T) {
	var gotQuery url.Values
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "mytoken")
	err := c.SetContentRating("99", PlexTypeEpisode, RatingR16)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotQuery.Get("contentRating.value") != "nz/R16" {
		t.Errorf("contentRating.value = %q; want nz/R16", gotQuery.Get("contentRating.value"))
	}
	if gotQuery.Get("contentRating.locked") != "1" {
		t.Errorf("contentRating.locked = %q; want 1", gotQuery.Get("contentRating.locked"))
	}
	if gotQuery.Get("type") != "4" {
		t.Errorf("type = %q; want 4 (PlexTypeEpisode)", gotQuery.Get("type"))
	}
}

func TestHTTPPlexClient_SetContentRating_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "bad-token")
	err := c.SetContentRating("1", PlexTypeShow, RatingM)
	if err == nil {
		t.Error("expected error for non-2xx response")
	}
}

func TestHTTPPlexClient_SetAudioLanguage_SendsCorrectParams(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "mytoken")
	err := c.SetAudioLanguage("99", AudioLangs{Default: "ja"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotPath != "/library/metadata/99/prefs" {
		t.Errorf("path = %q; want /library/metadata/99/prefs", gotPath)
	}
	// Sent as the region-qualified BCP-47 tag Plex Web uses, not bare "ja".
	if gotQuery.Get("audioLanguage") != "ja-JP" {
		t.Errorf("audioLanguage = %q; want ja-JP", gotQuery.Get("audioLanguage"))
	}
}

func TestPlexAudioLangTag(t *testing.T) {
	cases := map[string]string{
		"ja":    "ja-JP", // ISO 639-1 mapped to region-qualified
		"en":    "en-US",
		"ko":    "ko-KR",
		"JA":    "ja-JP", // case-insensitive
		"ja-JP": "ja-JP", // already region-qualified: passthrough
		"en-GB": "en-GB", // explicit region preserved
		"":      "",      // empty passthrough
		"xx":    "xx",    // unmapped falls back to input
	}
	for in, want := range cases {
		if got := plexAudioLangTag(in); got != want {
			t.Errorf("plexAudioLangTag(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestHTTPPlexClient_SetAudioLanguage_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "bad-token")
	err := c.SetAudioLanguage("1", AudioLangs{Default: "en"})
	if err == nil {
		t.Error("expected error for non-2xx response")
	}
}

// --- Legacy GUID format tests ---

func TestHTTPPlexClient_FindShowByTMDBID_LegacyGuidString(t *testing.T) {
	// Plex returns Guid as a plain string instead of an array (legacy agents).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "show"))
		} else {
			fmt.Fprint(w, plexAllShowsJSONLegacyGuid("42", "tmdb://12345"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	key, err := c.FindShowByTMDBID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "42" {
		t.Errorf("got ratingKey %q; want 42", key)
	}
}

func TestHTTPPlexClient_FindShowByTMDBID_LegacyLowercaseGuid(t *testing.T) {
	// Plex returns guid (lowercase) as "com.plexapp.agents.themoviedb://12345?lang=en".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "show"))
		} else {
			fmt.Fprint(w, plexAllShowsJSONLegacyLower("55", "com.plexapp.agents.themoviedb://12345?lang=en"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	key, err := c.FindShowByTMDBID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "55" {
		t.Errorf("got ratingKey %q; want 55", key)
	}
}

// --- GetContentRating tests ---

func plexTVMetadataJSON(contentRating string) string {
	return `{"MediaContainer":{"Metadata":[{"contentRating":"` + contentRating + `"}]}}`
}

func TestHTTPPlexClient_GetContentRating_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, plexTVMetadataJSON("nz/M"))
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	rating, err := c.GetContentRating("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingM {
		t.Errorf("got %q; want M", rating)
	}
}

func TestHTTPPlexClient_GetContentRating_UnknownRating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, plexTVMetadataJSON("TV-14"))
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	rating, err := c.GetContentRating("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("got %q; want RatingNone for unrecognised rating", rating)
	}
}

func TestHTTPPlexClient_GetContentRating_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"MediaContainer":{"Metadata":[]}}`)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	rating, err := c.GetContentRating("99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("got %q; want RatingNone when metadata is empty", rating)
	}
}

// --- FindShowByTitle tests ---

func TestHTTPPlexClient_FindShowByTitle_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "show"))
		} else {
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"42","title":"AIKa"}]}}`)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	key, err := c.FindShowByTitle("AIKa")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "42" {
		t.Errorf("got ratingKey %q; want 42", key)
	}
}

func TestHTTPPlexClient_FindShowByTitle_NormalisedMatch(t *testing.T) {
	// Plex title has punctuation; search title differs in case/punctuation.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "show"))
		} else {
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"55","title":"It's Always Sunny"}]}}`)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	key, err := c.FindShowByTitle("its always sunny")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "55" {
		t.Errorf("got ratingKey %q; want 55", key)
	}
}

func TestHTTPPlexClient_FindShowByTitle_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "show"))
		} else {
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"99","title":"Other Show"}]}}`)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	_, err := c.FindShowByTitle("AIKa")
	if err != ErrPlexNotFound {
		t.Errorf("expected ErrPlexNotFound, got %v", err)
	}
}

func TestHTTPPlexClient_FindShowByTitle_SkipsMovieSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "movie"))
		} else {
			t.Error("should not scan non-show section")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	_, err := c.FindShowByTitle("AIKa")
	if err != ErrPlexNotFound {
		t.Errorf("expected ErrPlexNotFound, got %v", err)
	}
}

// --- FindShowByFilePath tests ---

func TestHTTPPlexClient_FindShowByFilePath_Found(t *testing.T) {
	const filePath = `G:\TV\AIKa (1997)\Season 01\AIKa - S01E01 - Trial 1꞉ Beautiful Agent.mkv`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("2", "show"))
		} else {
			// Verify the part.file param is passed.
			if r.URL.Query().Get("part.file") != filePath {
				t.Errorf("part.file = %q; want %q", r.URL.Query().Get("part.file"), filePath)
			}
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"grandparentRatingKey":"23067"}]}}`)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	key, err := c.FindShowByFilePath(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "23067" {
		t.Errorf("got ratingKey %q; want 23067", key)
	}
}

func TestHTTPPlexClient_FindShowByFilePath_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("2", "show"))
		} else {
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[]}}`)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	_, err := c.FindShowByFilePath(`G:\TV\Unknown\S01E01.mkv`)
	if err != ErrPlexNotFound {
		t.Errorf("expected ErrPlexNotFound, got %v", err)
	}
}

func TestHTTPPlexClient_FindShowByFilePath_SkipsMovieSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexSectionsJSON("1", "movie"))
		} else {
			t.Error("should not scan non-show section")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	_, err := c.FindShowByFilePath(`G:\TV\Something\S01E01.mkv`)
	if err != ErrPlexNotFound {
		t.Errorf("expected ErrPlexNotFound, got %v", err)
	}
}

// --- RefreshLibraries tests ---

func TestHTTPPlexClient_RefreshLibraries_RefreshesShowSections(t *testing.T) {
	var refreshPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			// One show section and one movie section — only the show section should be refreshed.
			fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"show"},{"key":"2","type":"movie"}]}}`)
		} else {
			refreshPaths = append(refreshPaths, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	if err := c.RefreshLibraries(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refreshPaths) != 1 || refreshPaths[0] != "/library/sections/1/refresh" {
		t.Errorf("refreshPaths = %v; want [/library/sections/1/refresh]", refreshPaths)
	}
}

// --- RefreshMetadata tests ---

func TestHTTPPlexClient_RefreshMetadata_PutsCorrectPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	if err := c.RefreshMetadata("42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotPath != "/library/metadata/42/refresh" {
		t.Errorf("path = %q; want /library/metadata/42/refresh", gotPath)
	}
}

func TestHTTPPlexClient_RefreshMetadata_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	err := c.RefreshMetadata("42")
	if err == nil || !strings.Contains(err.Error(), "PUT") || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v; want a PUT 404 error", err)
	}
}

// --- guidStringMatchesTMDBID unit tests ---

func TestGuidStringMatchesTMDBID(t *testing.T) {
	tests := []struct {
		s, tmdbID string
		want      bool
	}{
		{"tmdb://12345", "12345", true},
		{"tmdb://12345?lang=en", "12345", true},
		{"com.plexapp.agents.themoviedb://12345?lang=en", "12345", true},
		{"tmdb://123456", "12345", false}, // partial match — must not fire
		{"tmdb://12345", "123456", false}, // different ID
		{"", "12345", false},              // empty string
		{"tmdb://12345", "", false},       // empty ID
	}
	for _, tc := range tests {
		got := guidStringMatchesTMDBID(tc.s, tc.tmdbID)
		if got != tc.want {
			t.Errorf("guidStringMatchesTMDBID(%q, %q) = %v; want %v", tc.s, tc.tmdbID, got, tc.want)
		}
	}
}

// --- multiPlexClient tests ---

func TestMultiPlexClient_SetAudioLanguage_CallsAll(t *testing.T) {
	primary := &fakePlexClient{}
	alt := &fakePlexClient{}
	alt2 := &fakePlexClient{}
	mc := newMultiPlexClient(primary,
		namedPlexClient{name: altAccount, client: alt},
		namedPlexClient{name: alt2Account, client: alt2})
	if err := mc.SetAudioLanguage("rk1", AudioLangs{Default: "ja"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for name, c := range map[string]*fakePlexClient{"primary": primary, "alt": alt, "alt2": alt2} {
		if len(c.setLangCalls) != 1 || c.setLangCalls[0] != (setLangCall{"rk1", "ja"}) {
			t.Errorf("%s calls: %+v", name, c.setLangCalls)
		}
	}
}

func TestMultiPlexClient_SetAudioLanguage_PerAccountOverrides(t *testing.T) {
	primary := &fakePlexClient{}
	alt := &fakePlexClient{}
	alt2 := &fakePlexClient{}
	mc := newMultiPlexClient(primary,
		namedPlexClient{name: altAccount, client: alt},
		namedPlexClient{name: alt2Account, client: alt2})
	// alt overrides the language; alt2 has none and falls back to the default.
	langs := AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en"}}
	if err := mc.SetAudioLanguage("rk1", langs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{"primary": "ja", "alt": "en", "alt2": "ja"}
	for name, c := range map[string]*fakePlexClient{"primary": primary, "alt": alt, "alt2": alt2} {
		if len(c.setLangCalls) != 1 || c.setLangCalls[0].lang != want[name] {
			t.Errorf("%s: got %+v; want lang %q", name, c.setLangCalls, want[name])
		}
	}
}

func TestMultiPlexClient_SetAudioLanguage_PrimaryErrorPropagates(t *testing.T) {
	primary := &fakePlexClient{setLangErr: errors.New("primary failed")}
	alt := &fakePlexClient{}
	mc := newMultiPlexClient(primary, namedPlexClient{name: altAccount, client: alt})
	if err := mc.SetAudioLanguage("rk1", AudioLangs{Default: "ja"}); err == nil {
		t.Fatal("expected error from primary")
	}
	if len(alt.setLangCalls) != 0 {
		t.Error("alt should not be called when primary fails")
	}
}

func TestMultiPlexClient_SetAudioLanguage_AltErrorStoredNotPropagated(t *testing.T) {
	primary := &fakePlexClient{}
	alt := &fakePlexClient{setLangErr: errors.New("alt failed")}
	alt2 := &fakePlexClient{setLangErr: errors.New("plex: set audio language got status 404: nope")}
	mc := newMultiPlexClient(primary,
		namedPlexClient{name: altAccount, client: alt},
		namedPlexClient{name: alt2Account, client: alt2})
	if err := mc.SetAudioLanguage("rk1", AudioLangs{Default: "ja"}); err != nil {
		t.Fatalf("alt error should not propagate: %v", err)
	}
	r, ok := mc.(multiAccountReporter)
	if !ok {
		t.Fatal("multiPlexClient should implement multiAccountReporter")
	}
	want := "; alt: alt failed; alt2: not in library"
	if got := r.altErrSuffix(); got != want {
		t.Errorf("altErrSuffix() = %q; want %q", got, want)
	}
}

func newTwoAltClient(primary, alt, alt2 *fakePlexClient) *multiPlexClient {
	return newMultiPlexClient(primary,
		namedPlexClient{name: altAccount, client: alt},
		namedPlexClient{name: alt2Account, client: alt2}).(*multiPlexClient)
}

// With everything succeeding, the record equals appliedSignature (region tags
// included), so processApplyLanguage's applied == sig check stops the next run.
func TestMultiPlexClient_SetLanguagePending_RecordMatchesSignature(t *testing.T) {
	primary, alt, alt2 := &fakePlexClient{}, &fakePlexClient{}, &fakePlexClient{}
	mc := newTwoAltClient(primary, alt, alt2)
	langs := AudioLangs{Default: "ja", Alts: map[string]string{alt2Account: "en"}}
	record, _, err := mc.setLanguagePending("rk1", langs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := mc.appliedSignature(langs); record != want || record != "ja-JP|alt=ja-JP|alt2=en-US" {
		t.Errorf("record = %q; want %q", record, want)
	}
	if _, _, err := mc.setLanguagePending("rk1", langs, record); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(primary.setLangCalls) != 1 || len(alt.setLangCalls) != 1 || len(alt2.setLangCalls) != 1 {
		t.Errorf("calls primary %d, alt %d, alt2 %d; want 1 each (nothing pending the second time)", len(primary.setLangCalls), len(alt.setLangCalls), len(alt2.setLangCalls))
	}
}

// One alt fails and the next still runs; the record keeps the primary and the
// alt that succeeded.
func TestMultiPlexClient_SetLanguagePending_PartialFailure(t *testing.T) {
	alt := &fakePlexClient{setLangErr: errors.New("alt failed")}
	alt2 := &fakePlexClient{}
	mc := newTwoAltClient(&fakePlexClient{}, alt, alt2)
	record, _, err := mc.setLanguagePending("rk1", AudioLangs{Default: "ja", Alts: map[string]string{alt2Account: "en"}}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alt2.setLangCalls) != 1 || record != "ja-JP|alt2=en-US" {
		t.Errorf("alt2 calls %d, record %q; want 1 and ja-JP|alt2=en-US", len(alt2.setLangCalls), record)
	}
	if got := mc.altErrSuffix(); got != "; alt: alt failed" {
		t.Errorf("altErrSuffix() = %q", got)
	}
}

// Only the accounts whose language changed are set again.
func TestMultiPlexClient_SetLanguagePending_ChangedLanguage(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		langs                AudioLangs
		wantPrimary, wantAlt int
		wantRecord           string
	}{
		{"primary changed, alt overridden", AudioLangs{Default: "ko", Alts: map[string]string{altAccount: "ja"}}, 1, 0, "ko-KR|alt=ja-JP"},
		{"alt changed", AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en"}}, 0, 1, "ja-JP|alt=en-US"},
		{"bare code matches cached tag", AudioLangs{Default: "ja"}, 0, 0, "ja-JP|alt=ja-JP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, alt := &fakePlexClient{}, &fakePlexClient{}
			mc := newMultiPlexClient(primary, namedPlexClient{name: altAccount, client: alt}).(*multiPlexClient)
			record, _, err := mc.setLanguagePending("rk1", tc.langs, "ja-JP|alt=ja-JP")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(primary.setLangCalls) != tc.wantPrimary || len(alt.setLangCalls) != tc.wantAlt {
				t.Errorf("calls primary %d, alt %d; want %d, %d", len(primary.setLangCalls), len(alt.setLangCalls), tc.wantPrimary, tc.wantAlt)
			}
			if record != tc.wantRecord {
				t.Errorf("record = %q; want %q", record, tc.wantRecord)
			}
		})
	}
}

// A pending primary that fails returns its error, records nothing and stops
// before the alts.
func TestMultiPlexClient_SetLanguagePending_PrimaryFailure(t *testing.T) {
	alt := &fakePlexClient{}
	mc := newMultiPlexClient(&fakePlexClient{setLangErr: errors.New("primary failed")}, namedPlexClient{name: altAccount, client: alt}).(*multiPlexClient)
	record, _, err := mc.setLanguagePending("rk1", AudioLangs{Default: "ja"}, "")
	if err == nil || record != "" || len(alt.setLangCalls) != 0 {
		t.Errorf("err %v, record %q, alt calls %d; want an error, no record, no alt call", err, record, len(alt.setLangCalls))
	}
}

func TestParseLangSignature(t *testing.T) {
	for _, tc := range []struct {
		sig  string
		want map[string]string
	}{
		{"", map[string]string{}},
		{"ja-JP", map[string]string{"": "ja-JP"}},
		{"ja-JP|alt=en-US|alt2=ja-JP", map[string]string{"": "ja-JP", "alt": "en-US", "alt2": "ja-JP"}},
		{"ja-JP|alt||alt2=en-US", map[string]string{"": "ja-JP", "alt2": "en-US"}}, // malformed entries dropped
	} {
		got := parseLangSignature(tc.sig)
		if len(got) != len(tc.want) {
			t.Errorf("parseLangSignature(%q) = %v; want %v", tc.sig, got, tc.want)
			continue
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("parseLangSignature(%q)[%q] = %q; want %q", tc.sig, k, got[k], v)
			}
		}
	}
}

func TestMultiPlexClient_SetAudioLanguage_AltErrClearedOnSuccess(t *testing.T) {
	primary := &fakePlexClient{}
	alt := &fakePlexClient{}
	mc := newMultiPlexClient(primary, namedPlexClient{name: altAccount, client: alt})
	// First call with alt error.
	alt.setLangErr = errors.New("alt failed")
	mc.SetAudioLanguage("rk1", AudioLangs{Default: "ja"}) //nolint
	// Second call with alt success — the stored error should be cleared.
	alt.setLangErr = nil
	mc.SetAudioLanguage("rk1", AudioLangs{Default: "ja"}) //nolint
	r := mc.(multiAccountReporter)
	if got := r.altErrSuffix(); got != "" {
		t.Errorf("altErrSuffix() should be empty after successful alt call, got %q", got)
	}
}

func TestNewMultiPlexClient_NoAltsReturnsPrimary(t *testing.T) {
	primary := &fakePlexClient{}
	mc := newMultiPlexClient(primary)
	if mc != PlexClient(primary) {
		t.Error("expected primary to be returned when there are no alts")
	}
}

func TestAppliedLangSignature(t *testing.T) {
	alts := []namedPlexClient{
		{name: altAccount, client: &fakePlexClient{}},
		{name: alt2Account, client: &fakePlexClient{}},
	}
	cases := []struct {
		name  string
		alts  []namedPlexClient
		langs AudioLangs
		want  string
	}{
		{
			// Region-qualified tag, matching what is actually sent to Plex.
			"no alt accounts", nil, AudioLangs{Default: "ja"}, "ja-JP",
		},
		{
			// Listed even though they inherit "ja-JP" — otherwise adding a token
			// would leave already-applied titles skipped forever.
			"alt accounts inheriting the default", alts, AudioLangs{Default: "ja"},
			"ja-JP|alt=ja-JP|alt2=ja-JP",
		},
		{
			"one account overriding", alts,
			AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en"}},
			"ja-JP|alt=en-US|alt2=ja-JP",
		},
		{
			"both accounts overriding", alts,
			AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en", alt2Account: "fr"}},
			"ja-JP|alt=en-US|alt2=fr-FR",
		},
		{
			"only alt2 configured", alts[1:],
			AudioLangs{Default: "ja", Alts: map[string]string{alt2Account: "fr"}},
			"ja-JP|alt2=fr-FR",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newMultiPlexClient(&fakePlexClient{}, tc.alts...)
			if got := appliedLangSignature(client, tc.langs); got != tc.want {
				t.Errorf("appliedLangSignature = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestAudioLangs_ForAccount_FallsBackToDefault(t *testing.T) {
	langs := AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en"}}
	if got := langs.forAccount(altAccount); got != "en" {
		t.Errorf("forAccount(alt) = %q; want en", got)
	}
	if got := langs.forAccount(alt2Account); got != "ja" {
		t.Errorf("forAccount(alt2) = %q; want ja (default)", got)
	}
}

func TestManualEntry_AudioLangs(t *testing.T) {
	// No "language": TMDB's original_language is the default.
	got := ManualEntry{AltLanguage: "en"}.audioLangs("ja")
	if got.Default != "ja" || got.forAccount(altAccount) != "en" || got.forAccount(alt2Account) != "ja" {
		t.Errorf("tmdb fallback: %+v", got)
	}
	// "language" overrides TMDB, and alts fall back to it rather than to TMDB.
	got = ManualEntry{Language: "ko", Alt2Language: "fr"}.audioLangs("ja")
	if got.Default != "ko" || got.forAccount(altAccount) != "ko" || got.forAccount(alt2Account) != "fr" {
		t.Errorf("language override: %+v", got)
	}
}

func TestAltPlexClients_ReadsBothTokens(t *testing.T) {
	t.Setenv("PLEX_ALT_TOKEN", "")
	t.Setenv("PLEX_ALT2_TOKEN", "")
	if got := altPlexClients("http://plex"); got != nil {
		t.Errorf("no tokens set: got %d clients; want 0", len(got))
	}

	t.Setenv("PLEX_ALT2_TOKEN", "tok2")
	got := altPlexClients("http://plex")
	if len(got) != 1 || got[0].name != "alt2" {
		t.Fatalf("only PLEX_ALT2_TOKEN set: got %+v; want one client named alt2", got)
	}

	t.Setenv("PLEX_ALT_TOKEN", "tok1")
	got = altPlexClients("http://plex")
	if len(got) != 2 || got[0].name != "alt" || got[1].name != "alt2" {
		t.Fatalf("both tokens set: got %+v; want alt then alt2", got)
	}
}

// An error status with a JSON body would decode as an empty result: "show not
// found" or "no rating" instead of an error.
func TestHTTPPlexClient_ErrorStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := newHTTPPlexClient(srv.URL, "secrettoken")

	calls := map[string]func() error{
		"FindShowByTMDBID": func() error { _, err := c.FindShowByTMDBID("1"); return err },
		"GetContentRating": func() error { _, err := c.GetContentRating("1"); return err },
		"GetSeasons":       func() error { _, err := c.GetSeasons("1"); return err },
	}
	for name, call := range calls {
		err := call()
		if err == nil || errors.Is(err, ErrPlexNotFound) {
			t.Errorf("%s: err = %v, want a status error", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secrettoken") {
			t.Errorf("%s: error %q should name the status and not leak the token", name, err)
		}
	}
}
