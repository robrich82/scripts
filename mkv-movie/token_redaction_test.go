package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const secretToken = "s3cret-token"

// plexTokenServer fails the test if a request carries the token in its URL or
// lacks it in the X-Plex-Token header.
func plexTokenServer(t *testing.T, requests *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		if strings.Contains(r.URL.RawQuery, secretToken) || r.URL.Query().Has("X-Plex-Token") {
			t.Errorf("%s %s: token in URL", r.Method, r.URL)
		}
		if got := r.Header.Get("X-Plex-Token"); got != secretToken {
			t.Errorf("%s %s: X-Plex-Token header = %q", r.Method, r.URL.Path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// closedServerURL returns the URL of a server that is no longer listening, so
// requests to it fail in the transport with a *url.Error.
func closedServerURL() string {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

func TestPlexMovieClient_SendsTokenInHeader(t *testing.T) {
	requests := 0
	c := newHTTPPlexMovieClient(plexTokenServer(t, &requests).URL, secretToken)
	_, _ = c.FindMovieByTMDBID("12345")
	if err := c.SetContentRating("77", NZRating("M")); err != nil {
		t.Errorf("SetContentRating: %v", err)
	}
	if err := c.plexSetStream(5, "audioStreamID", 9); err != nil {
		t.Errorf("plexSetStream: %v", err)
	}
	if requests < 3 {
		t.Errorf("server got %d requests, want at least 3", requests)
	}
}

func TestPlexMovieClient_TransportErrorOmitsToken(t *testing.T) {
	c := newHTTPPlexMovieClient(closedServerURL(), secretToken)
	_, getErr := c.FindMovieByTMDBID("12345")
	putErr := c.SetContentRating("77", NZRating("M"))
	for _, err := range []error{getErr, putErr} {
		if err == nil {
			t.Fatal("expected a connection error")
		}
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("error leaks the token: %v", err)
		}
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestTMDBMovieSource_TransportErrorOmitsAPIKey(t *testing.T) {
	src := &tmdbMovieSource{apiKey: secretToken, httpClient: &http.Client{Transport: failingTransport{}}}
	_, searchErr := src.SearchMovieID("Heat", "1995")
	_, certErr := src.nzCertification("949")
	_, detailErr := src.GetMovieDetails("949")
	for _, err := range []error{searchErr, certErr, detailErr} {
		if err == nil {
			t.Fatal("expected a transport error")
		}
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("error leaks the API key: %v", err)
		}
		if !strings.Contains(err.Error(), "api_key=REDACTED") {
			t.Errorf("error = %v, want the URL kept with api_key=REDACTED", err)
		}
	}
}
