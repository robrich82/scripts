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
