package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// errorTMDBSource returns a tmdbMovieSource whose requests all reach a server
// answering with status and TMDB's JSON error body.
func errorTMDBSource(t *testing.T, status int) *tmdbMovieSource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status_code":7,"status_message":"Invalid API key: You must be granted a valid key."}`))
	}))
	t.Cleanup(srv.Close)
	return &tmdbMovieSource{apiKey: "secretkey", httpClient: &http.Client{Transport: &redirectTransport{target: srv.URL}}}
}

func TestTMDBMovieSource_ErrorStatus_ReturnsError(t *testing.T) {
	src := errorTMDBSource(t, http.StatusUnauthorized)
	calls := map[string]func() error{
		"SearchAndFetch":  func() error { _, _, err := src.SearchAndFetch("Inception", "2010"); return err },
		"GetMovieDetails": func() error { _, err := src.GetMovieDetails("27205"); return err },
		"FetchRating":     func() error { _, err := src.FetchRating("27205", "Inception"); return err },
	}
	for name, call := range calls {
		err := call()
		if err == nil {
			t.Errorf("%s: want an error for a 401 response", name)
			continue
		}
		if !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secretkey") {
			t.Errorf("%s: error %q should name the status and not leak the key", name, err)
		}
	}
}

// A TMDB rate-limit response counts the file as an error rather than skipping
// it as "no release date" or "not found on TMDB".
func TestWalkAndProcess_TMDBRateLimited_CountsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Inception.2010.mkv"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	cache := makeCache(t)
	stats := walkAndProcess(dir, errorTMDBSource(t, http.StatusTooManyRequests), nil, &fakeFileTimer{}, nil, cache, nil, false, time.Time{})
	if stats.Errors != 1 || stats.Skipped != 0 {
		t.Errorf("got Errors=%d Skipped=%d, want 1 and 0", stats.Errors, stats.Skipped)
	}
	if id := cache.GetTMDBID("Inception:2010"); id != "" {
		t.Errorf("cached TMDB ID %q from an error response", id)
	}
}
