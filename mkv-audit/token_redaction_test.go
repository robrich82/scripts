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
