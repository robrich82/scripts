package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newPageServer starts a TLS server for checker pages; its client trusts it.
func newPageServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func htmlApp(pageURL string) App {
	return App{
		Name: "testapp",
		Checker: CheckerConfig{
			Type:          "html",
			URL:           pageURL,
			VersionRegex:  `version (\d+\.\d+)`,
			AssetPatterns: []string{"*.exe"},
		},
	}
}

func TestCheckerRefusesRedirectToHTTP(t *testing.T) {
	calls := 0
	plain := plainServer(t, &calls)
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/downloads", http.StatusFound)
	})

	_, err := NewChecker(srv.Client()).Check(htmlApp(srv.URL + "/downloads"))
	var insecure *insecureURLError
	if !errors.As(err, &insecure) {
		t.Errorf("err = %v, want an insecureURLError", err)
	}
	if calls != 0 {
		t.Errorf("plain-http server got %d requests, want none", calls)
	}
}

func TestCheckerFollowsHTTPSRedirect(t *testing.T) {
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
			return
		}
		fmt.Fprint(w, `version 2.5 <a href="/files/app-2.5.exe">get</a>`)
	})

	result, err := NewChecker(srv.Client()).Check(htmlApp(srv.URL + "/old"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Version != "2.5" {
		t.Errorf("version = %q, want 2.5", result.Version)
	}
	if want := srv.URL + "/files/app-2.5.exe"; len(result.Assets) != 1 || result.Assets[0].URL != want {
		t.Errorf("assets = %v, want one at %s", result.Assets, want)
	}
}

func TestCheckerRejectsHTTPAssetURL(t *testing.T) {
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `version 2.5 <a href="http://mirror.example.com/app-2.5.exe">get</a>`)
	})

	result, err := NewChecker(srv.Client()).Check(htmlApp(srv.URL + "/"))
	var insecure *insecureURLError
	if !errors.As(err, &insecure) {
		t.Fatalf("err = %v, result = %v, want an insecureURLError", err, result)
	}
	if insecure.URL != "http://mirror.example.com/app-2.5.exe" {
		t.Errorf("URL = %q, want the http asset", insecure.URL)
	}
}

func TestNewCheckerLeavesCallerClientAlone(t *testing.T) {
	client := &http.Client{}
	NewChecker(client)
	if client.CheckRedirect != nil {
		t.Error("NewChecker set CheckRedirect on the caller's client")
	}
}
