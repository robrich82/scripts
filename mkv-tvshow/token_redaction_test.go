package main

import (
	"fmt"
	"io"
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
		fmt.Fprint(w, `{"MediaContainer":{}}`)
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

	_, _ = newHTTPPlexClient(plex.URL, secretToken).FindShowByTMDBID("1399")
	if len(sameHostTokens) == 0 || sameHostTokens[0] != secretToken {
		t.Errorf("same-host redirect tokens = %q, want %q kept", sameHostTokens, secretToken)
	}
	if otherRequests == 0 {
		t.Error("the redirect to the other host was not followed")
	}
}

// IDs come from prompts and caches; one that breaks url.Parse or adds its own
// query must not let the key through.
func TestTMDBClient_OddIDOmitsAPIKey(t *testing.T) {
	for _, id := range []string{"12%zz", "1399?x=1", "1399#x"} {
		_, err := newHTTPTMDBClient(secretToken, closedServerURL()).GetShow(id)
		if err == nil {
			t.Fatalf("id %q: expected an error", id)
		}
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("id %q: error leaks the API key: %v", id, err)
		}
		if !strings.Contains(err.Error(), "api_key=REDACTED") {
			t.Errorf("id %q: error = %v, want the URL kept with api_key=REDACTED", id, err)
		}
	}
}

func TestHistoryClients_SendTokenInHeader(t *testing.T) {
	requests := 0
	srv := plexTokenServer(t, &requests)
	c := newHTTPPlexClient(srv.URL, secretToken)
	if _, err := c.Accounts(); err != nil {
		t.Errorf("Accounts: %v", err)
	}
	if _, err := c.WatchHistory(1); err != nil {
		t.Errorf("WatchHistory: %v", err)
	}
	if err := c.DeleteHistory(historyKeyPrefix + "1"); err != nil {
		t.Errorf("DeleteHistory: %v", err)
	}
	_, _ = newPlexTVClient(srv.URL, srv.URL, secretToken).User() // no username in the reply
	if requests != 4 {
		t.Errorf("server got %d requests, want 4", requests)
	}
}

func TestHistoryClients_TransportErrorOmitsToken(t *testing.T) {
	c := newHTTPPlexClient(closedServerURL(), secretToken)
	_, accountsErr := c.Accounts()
	_, historyErr := c.WatchHistory(1)
	_, userErr := newPlexTVClient(closedServerURL(), closedServerURL(), secretToken).User()
	for _, err := range []error{accountsErr, historyErr, c.DeleteHistory(historyKeyPrefix + "1"), userErr} {
		if err == nil {
			t.Fatal("expected a connection error")
		}
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("error leaks the token: %v", err)
		}
	}
}

func TestPlexTVGraphQL_TransportErrorOmitsToken(t *testing.T) {
	c := newPlexTVClient(closedServerURL(), closedServerURL(), secretToken)
	_, historyErr := c.CloudHistoryPage("u-1", "")
	for _, err := range []error{historyErr, c.RemoveActivity("e1")} {
		if err == nil {
			t.Fatal("expected a connection error")
		}
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("error leaks the token: %v", err)
		}
	}
}

// A 307 keeps the POST and its body, but not the token on another host; a
// 302 would resend the mutation as a GET, so it's refused.
func TestPlexTVGraphQL_Redirects(t *testing.T) {
	var otherBody string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Plex-Token"); got != "" {
			t.Errorf("other host got X-Plex-Token %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		otherBody = r.Method + " " + string(b)
		fmt.Fprint(w, `{"data":{"removeActivity":true}}`)
	}))
	t.Cleanup(other.Close)
	status := http.StatusTemporaryRedirect
	community := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api", status)
	}))
	t.Cleanup(community.Close)
	c := newPlexTVClient(community.URL, community.URL, secretToken)

	if err := c.RemoveActivity("e1"); err != nil {
		t.Fatalf("307: %v", err)
	}
	if !strings.HasPrefix(otherBody, "POST ") || !strings.Contains(otherBody, `"id":"e1"`) {
		t.Errorf("other host got %q; want the POST with its body", otherBody)
	}
	status = http.StatusFound
	if err := c.RemoveActivity("e2"); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Errorf("302: err = %v; want the redirect refused", err)
	}
}
