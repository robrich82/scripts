package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func plexMovieSectionsJSON(sectionKey, sectionType string) string {
	return `{"MediaContainer":{"Directory":[{"key":"` + sectionKey + `","type":"` + sectionType + `"}]}}`
}

func plexAllMoviesJSON(ratingKey, tmdbGUID string) string {
	return `{"MediaContainer":{"Metadata":[{"ratingKey":"` + ratingKey + `","Guid":[{"id":"` + tmdbGUID + `"}]}]}}`
}

func plexAllMoviesJSONLegacyGuid(ratingKey, guidStr string) string {
	return `{"MediaContainer":{"Metadata":[{"ratingKey":"` + ratingKey + `","Guid":"` + guidStr + `"}]}}`
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(plexAllMoviesJSON("77", "tmdb://12345")))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	key, err := c.FindMovieByTMDBID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "77" {
		t.Errorf("got ratingKey %q; want 77", key)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(plexAllMoviesJSON("99", "tmdb://99999")))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	_, err := c.FindMovieByTMDBID("12345")
	if err != ErrPlexMovieNotFound {
		t.Errorf("expected ErrPlexMovieNotFound, got %v", err)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_SkipsShowSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			// Only a "show" section — should be skipped.
			w.Write([]byte(plexMovieSectionsJSON("1", "show")))
		} else {
			t.Error("should not scan non-movie section")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	_, err := c.FindMovieByTMDBID("12345")
	if err != ErrPlexMovieNotFound {
		t.Errorf("expected ErrPlexMovieNotFound, got %v", err)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_LegacyGuid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(plexAllMoviesJSONLegacyGuid("88", "tmdb://12345")))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	key, err := c.FindMovieByTMDBID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "88" {
		t.Errorf("got ratingKey %q; want 88", key)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_CachesResult(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(plexAllMoviesJSON("77", "tmdb://12345")))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	c.FindMovieByTMDBID("12345")
	after := calls
	c.FindMovieByTMDBID("12345") // should use cache
	if calls != after {
		t.Errorf("second FindMovieByTMDBID made HTTP requests")
	}
}

func TestHTTPPlexMovieClient_SetContentRating_SendsCorrectParams(t *testing.T) {
	var gotQuery url.Values
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "mytoken")
	err := c.SetContentRating("42", RatingM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotQuery.Get("contentRating.value") != "nz/M" {
		t.Errorf("contentRating.value = %q; want nz/M", gotQuery.Get("contentRating.value"))
	}
	if gotQuery.Get("contentRating.locked") != "1" {
		t.Errorf("contentRating.locked = %q; want 1", gotQuery.Get("contentRating.locked"))
	}
	if gotQuery.Get("type") != "1" {
		t.Errorf("type = %q; want 1 (movie)", gotQuery.Get("type"))
	}
}

func TestHTTPPlexMovieClient_SetAudioLanguage_SendsCorrectParams(t *testing.T) {
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

	c := newHTTPPlexMovieClient(srv.URL, "mytoken")
	err := c.SetAudioLanguage("42", "ja")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotPath != "/library/metadata/42/prefs" {
		t.Errorf("path = %q; want /library/metadata/42/prefs", gotPath)
	}
	if gotQuery.Get("audioLanguage") != "ja" {
		t.Errorf("audioLanguage = %q; want ja", gotQuery.Get("audioLanguage"))
	}
}

func TestHTTPPlexMovieClient_SetAudioLanguage_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "bad-token")
	if err := c.SetAudioLanguage("1", "en"); err == nil {
		t.Error("expected error for non-2xx response")
	}
}

func plexMetadataJSON(contentRating string) string {
	return `{"MediaContainer":{"Metadata":[{"contentRating":"` + contentRating + `"}]}}`
}

func TestHTTPPlexMovieClient_GetContentRating_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(plexMetadataJSON("nz/R16")))
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	rating, err := c.GetContentRating("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingR16 {
		t.Errorf("got %q; want R16", rating)
	}
}

func TestHTTPPlexMovieClient_GetContentRating_UnknownRating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(plexMetadataJSON("TV-MA")))
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	rating, err := c.GetContentRating("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("got %q; want RatingNone for unrecognised rating", rating)
	}
}

func TestHTTPPlexMovieClient_GetContentRating_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No Metadata entries at all.
		w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	rating, err := c.GetContentRating("99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("got %q; want RatingNone when metadata is empty", rating)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTitle_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"55","title":"The Matrix","year":1999}]}}`))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	key, err := c.FindMovieByTitle("The Matrix", "1999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "55" {
		t.Errorf("got ratingKey %q; want 55", key)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTitle_NormalisedMatch(t *testing.T) {
	// Title in Plex has punctuation; search title differs in case and punctuation.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("3", "movie")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"66","title":"It's a Wonderful Life","year":1946}]}}`))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	key, err := c.FindMovieByTitle("its a wonderful life", "1946")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "66" {
		t.Errorf("got ratingKey %q; want 66", key)
	}
}

func TestHTTPPlexMovieClient_RefreshLibraries_RefreshesMovieSections(t *testing.T) {
	var refreshPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			// One movie section and one show section — only the movie section should be refreshed.
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie"},{"key":"2","type":"show"}]}}`))
		} else {
			refreshPaths = append(refreshPaths, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	if err := c.RefreshLibraries(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refreshPaths) != 1 || refreshPaths[0] != "/library/sections/1/refresh" {
		t.Errorf("refreshPaths = %v; want [/library/sections/1/refresh]", refreshPaths)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTitle_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"77","title":"Other Film","year":2000}]}}`))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	_, err := c.FindMovieByTitle("The Matrix", "1999")
	if err != ErrPlexMovieNotFound {
		t.Errorf("expected ErrPlexMovieNotFound, got %v", err)
	}
}
