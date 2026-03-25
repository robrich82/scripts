package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// --- fakePlexClient for use in processor tests ---

type setRatingCall struct {
	ratingKey string
	itemType  PlexItemType
	rating    NZRating
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

func (f *fakePlexClient) RefreshLibraries() error  { return nil }
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

func TestHTTPPlexClient_FindShowByTMDBID_Found(t *testing.T) {
	var gotSectionPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexSectionsJSON("1", "show")))
		} else {
			gotSectionPath = r.URL.Path
			w.Write([]byte(plexAllShowsJSON("42", "tmdb://12345")))
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
			w.Write([]byte(plexSectionsJSON("1", "show")))
		} else {
			// Show present but different TMDB ID.
			w.Write([]byte(plexAllShowsJSON("99", "tmdb://99999")))
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
			w.Write([]byte(plexSectionsJSON("1", "movie")))
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
			w.Write([]byte(plexSectionsJSON("1", "show")))
		} else {
			w.Write([]byte(plexAllShowsJSON("42", "tmdb://12345")))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	c.FindShowByTMDBID("12345")
	callsAfterFirst := calls
	c.FindShowByTMDBID("12345") // should use cache
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
		w.Write([]byte(body))
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
		w.Write([]byte(body))
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

// --- Legacy GUID format tests ---

func TestHTTPPlexClient_FindShowByTMDBID_LegacyGuidString(t *testing.T) {
	// Plex returns Guid as a plain string instead of an array (legacy agents).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexSectionsJSON("1", "show")))
		} else {
			w.Write([]byte(plexAllShowsJSONLegacyGuid("42", "tmdb://12345")))
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
			w.Write([]byte(plexSectionsJSON("1", "show")))
		} else {
			w.Write([]byte(plexAllShowsJSONLegacyLower("55", "com.plexapp.agents.themoviedb://12345?lang=en")))
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
		w.Write([]byte(plexTVMetadataJSON("nz/M")))
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
		w.Write([]byte(plexTVMetadataJSON("TV-14")))
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
		w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
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
			w.Write([]byte(plexSectionsJSON("1", "show")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"42","title":"AIKa"}]}}`))
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
			w.Write([]byte(plexSectionsJSON("1", "show")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"55","title":"It's Always Sunny"}]}}`))
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
			w.Write([]byte(plexSectionsJSON("1", "show")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"99","title":"Other Show"}]}}`))
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
			w.Write([]byte(plexSectionsJSON("1", "movie")))
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
			w.Write([]byte(plexSectionsJSON("2", "show")))
		} else {
			// Verify the part.file param is passed.
			if r.URL.Query().Get("part.file") != filePath {
				t.Errorf("part.file = %q; want %q", r.URL.Query().Get("part.file"), filePath)
			}
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"grandparentRatingKey":"23067"}]}}`))
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
			w.Write([]byte(plexSectionsJSON("2", "show")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
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
			w.Write([]byte(plexSectionsJSON("1", "movie")))
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
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"show"},{"key":"2","type":"movie"}]}}`))
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

func TestHTTPPlexClient_RefreshMetadata_CallsCorrectPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, "token")
	if err := c.RefreshMetadata("42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/library/metadata/42/refresh" {
		t.Errorf("path = %q; want /library/metadata/42/refresh", gotPath)
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
