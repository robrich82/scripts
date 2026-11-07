package main

import (
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

func TestPlexClient_SendsTokenInHeader(t *testing.T) {
	requests := 0
	c := newHTTPPlexClient(plexTokenServer(t, &requests).URL, secretToken)
	_, _ = c.FindShowByTMDBID("1399")
	if err := c.SetEpisodeMetadata("77", "Pilot", "2011-04-17"); err != nil {
		t.Errorf("SetEpisodeMetadata: %v", err)
	}
	if err := c.SetAudioLanguage("77", AudioLangs{Default: "jpn"}); err != nil {
		t.Errorf("SetAudioLanguage: %v", err)
	}
	if err := c.SetContentRating("77", PlexTypeEpisode, NZRating("M")); err != nil {
		t.Errorf("SetContentRating: %v", err)
	}
	if requests < 4 {
		t.Errorf("server got %d requests, want at least 4", requests)
	}
}

func TestPlexClient_TransportErrorOmitsToken(t *testing.T) {
	c := newHTTPPlexClient(closedServerURL(), secretToken)
	_, getErr := c.FindShowByTMDBID("1399")
	errs := []error{
		getErr,
		c.SetEpisodeMetadata("77", "Pilot", ""),
		c.SetAudioLanguage("77", AudioLangs{Default: "jpn"}),
		c.SetContentRating("77", PlexTypeEpisode, NZRating("M")),
	}
	for _, err := range errs {
		if err == nil {
			t.Fatal("expected a connection error")
		}
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("error leaks the token: %v", err)
		}
	}
}

func TestTMDBClient_TransportErrorOmitsAPIKey(t *testing.T) {
	c := newHTTPTMDBClient(secretToken, closedServerURL())
	_, err := c.GetShow("1399")
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("error leaks the API key: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key=REDACTED") {
		t.Errorf("error = %v, want the URL kept with api_key=REDACTED", err)
	}
}
