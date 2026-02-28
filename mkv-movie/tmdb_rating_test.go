package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tmdbSearchJSON(id int, releaseDate string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"results": []map[string]interface{}{
			{"id": id, "release_date": releaseDate},
		},
	})
	return string(b)
}

func tmdbReleaseDatesJSON(iso, cert string, certType int) string {
	b, _ := json.Marshal(map[string]interface{}{
		"results": []map[string]interface{}{
			{
				"iso_3166_1": iso,
				"release_dates": []map[string]interface{}{
					{"certification": cert, "type": certType},
				},
			},
		},
	})
	return string(b)
}

func TestTMDBMovieSource_FetchRating_FoundNZCert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/search/movie") {
			w.Write([]byte(tmdbSearchJSON(12345, "2019-04-26")))
		} else {
			w.Write([]byte(tmdbReleaseDatesJSON("NZ", "M", 3)))
		}
	}))
	defer srv.Close()

	src := &tmdbMovieSource{apiKey: "key", httpClient: srv.Client()}
	src2 := &tmdbMovieSource{apiKey: "key", httpClient: srv.Client()}
	_ = src2
	// Patch base URL for test.
	origBase := tmdbAPIBase
	defer func() { /* restore not needed — const */ _ = origBase }()

	// Use a custom client that redirects to test server.
	src.httpClient = &http.Client{
		Transport: &redirectTransport{target: srv.URL},
	}

	rating, err := src.FetchRating("title:2019", "Avengers Endgame")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingM {
		t.Errorf("got %q; want M", rating)
	}
}

func TestTMDBMovieSource_FetchRating_NoAPIKey_ReturnsNone(t *testing.T) {
	src := newTMDBMovieSource("")
	r, err := src.FetchRating("title:2020", "Some Movie")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != RatingNone {
		t.Errorf("expected RatingNone, got %q", r)
	}
}

func TestTMDBMovieSource_FetchRating_NZAbsent_ReturnsNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/search/movie") {
			w.Write([]byte(tmdbSearchJSON(99, "2020-01-01")))
		} else {
			// Only US, no NZ.
			w.Write([]byte(tmdbReleaseDatesJSON("US", "PG-13", 3)))
		}
	}))
	defer srv.Close()

	src := &tmdbMovieSource{apiKey: "key", httpClient: &http.Client{
		Transport: &redirectTransport{target: srv.URL},
	}}
	r, err := src.FetchRating("title:2020", "Some Movie")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != RatingNone {
		t.Errorf("expected RatingNone when NZ cert absent, got %q", r)
	}
}

func TestTMDBMovieSource_FetchRating_NumericKey_SkipsSearch(t *testing.T) {
	searchCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/search/movie") {
			searchCalled = true
		}
		w.Write([]byte(tmdbReleaseDatesJSON("NZ", "R16", 3)))
	}))
	defer srv.Close()

	src := &tmdbMovieSource{apiKey: "key", httpClient: &http.Client{
		Transport: &redirectTransport{target: srv.URL},
	}}
	r, err := src.FetchRating("12345", "Ignored Title")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if searchCalled {
		t.Error("search should not be called when key is a numeric TMDB ID")
	}
	if r != RatingR16 {
		t.Errorf("got %q; want R16", r)
	}
}

func TestTitleFromFilename(t *testing.T) {
	tests := []struct {
		filename, year, want string
	}{
		{"The.Dark.Knight.2008.1080p.BluRay.mkv", "2008", "The Dark Knight"},
		{"Avengers.Endgame.2019.mkv", "2019", "Avengers Endgame"},
		{"The_Dark_Knight_2008.mkv", "2008", "The Dark Knight"},
		{"Movie.Title.(2020).mkv", "2020", "Movie Title"},
		{"NoYearHere.mkv", "", "NoYearHere"},
		{"film.mkv", "2021", "film"},
	}
	for _, tc := range tests {
		got := titleFromFilename(tc.filename, tc.year)
		if got != tc.want {
			t.Errorf("titleFromFilename(%q, %q) = %q; want %q", tc.filename, tc.year, got, tc.want)
		}
	}
}

// redirectTransport rewrites all requests to target (for httptest servers).
type redirectTransport struct {
	target string
	base   http.RoundTripper
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"
	newReq.URL.Host = rt.target[len("http://"):]
	if rt.base != nil {
		return rt.base.RoundTrip(newReq)
	}
	return http.DefaultTransport.RoundTrip(newReq)
}
