package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestTMDBOriginClient_TransportErrorOmitsAPIKey(t *testing.T) {
	const key = "s3cret-key"
	c := &httpTMDBOriginClient{apiKey: key, client: &http.Client{Transport: failingTransport{}}}
	_, _, tvErr := c.GetTVOriginByID("1399")
	_, _, movieErr := c.GetMovieOriginByID("949")
	for _, err := range []error{tvErr, movieErr} {
		if err == nil {
			t.Fatal("expected a transport error")
		}
		if strings.Contains(err.Error(), key) {
			t.Errorf("error leaks the API key: %v", err)
		}
		if !strings.Contains(err.Error(), "api_key=REDACTED") {
			t.Errorf("error = %v, want the URL kept with api_key=REDACTED", err)
		}
	}
}

// IDs come from prompts and caches; one that breaks url.Parse or adds its own
// query must not let the key through.
func TestTMDBOriginClient_OddIDOmitsAPIKey(t *testing.T) {
	for _, id := range []string{"12%zz", "1399?x=1", "1399#x"} {
		c := &httpTMDBOriginClient{apiKey: "s3cret-key", client: &http.Client{Transport: failingTransport{}}}
		_, _, err := c.GetTVOriginByID(id)
		if err == nil {
			t.Fatalf("id %q: expected an error", id)
		}
		if strings.Contains(err.Error(), "s3cret-key") {
			t.Errorf("id %q: error leaks the API key: %v", id, err)
		}
		if !strings.Contains(err.Error(), "api_key=REDACTED") {
			t.Errorf("id %q: error = %v, want the URL kept with api_key=REDACTED", id, err)
		}
	}
}
