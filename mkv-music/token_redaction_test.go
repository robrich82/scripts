package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const secretToken = "s3cret-token"

func TestPlexClient_SendsTokenInHeader(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if strings.Contains(r.URL.RawQuery, secretToken) || r.URL.Query().Has("X-Plex-Token") {
			t.Errorf("%s %s: token in URL", r.Method, r.URL)
		}
		if got := r.Header.Get("X-Plex-Token"); got != secretToken {
			t.Errorf("%s %s: X-Plex-Token header = %q", r.Method, r.URL.Path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"MediaContainer":{}}`)
	}))
	defer srv.Close()

	c := newHTTPPlexClient(srv.URL, secretToken)
	_, _ = c.FindArtistByTitle("Artist")
	if err := c.RefreshMetadata("77"); err != nil {
		t.Errorf("RefreshMetadata: %v", err)
	}
	if err := c.AnalyzeMedia("77"); err != nil {
		t.Errorf("AnalyzeMedia: %v", err)
	}
	if requests < 3 {
		t.Errorf("server got %d requests, want at least 3", requests)
	}
}

func TestPlexClient_TransportErrorOmitsToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // requests now fail in the transport with a *url.Error

	c := newHTTPPlexClient(srv.URL, secretToken)
	_, getErr := c.FindArtistByTitle("Artist")
	for _, err := range []error{getErr, c.AnalyzeMedia("77")} {
		if err == nil {
			t.Fatal("expected a connection error")
		}
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("error leaks the token: %v", err)
		}
	}
}

// Go forwards custom headers on a redirect to another host, so the Plex client
// must drop X-Plex-Token itself; a same-host redirect keeps it.
func TestPlexClient_RedirectToOtherHostDropsToken(t *testing.T) {
	otherRequests := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherRequests++
		if got := r.Header.Get("X-Plex-Token"); got != "" {
			t.Errorf("other host got X-Plex-Token %q", got)
		}
		fmt.Fprint(w, `{"MediaContainer":{}}`)
	}))
	t.Cleanup(other.Close)
	sameHostTokens := []string{}
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/same") {
			http.Redirect(w, r, "/same"+r.URL.RequestURI(), http.StatusFound)
			return
		}
		sameHostTokens = append(sameHostTokens, r.Header.Get("X-Plex-Token"))
		http.Redirect(w, r, other.URL+strings.TrimPrefix(r.URL.RequestURI(), "/same"), http.StatusFound)
	}))
	t.Cleanup(plex.Close)

	_, _ = newHTTPPlexClient(plex.URL, secretToken).FindArtistByTitle("Artist")
	if len(sameHostTokens) == 0 || sameHostTokens[0] != secretToken {
		t.Errorf("same-host redirect tokens = %q, want %q kept", sameHostTokens, secretToken)
	}
	if otherRequests == 0 {
		t.Error("the redirect to the other host was not followed")
	}
}
