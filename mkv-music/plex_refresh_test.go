package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPPlexClient_RefreshMetadata_PutsCorrectPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := newHTTPPlexClient(srv.URL, "token").RefreshMetadata("42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotPath != "/library/metadata/42/refresh" {
		t.Errorf("path = %q; want /library/metadata/42/refresh", gotPath)
	}
}

func TestHTTPPlexClient_RefreshMetadata_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	err := newHTTPPlexClient(srv.URL, "token").RefreshMetadata("42")
	if err == nil || !strings.Contains(err.Error(), "PUT") || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v; want a PUT 404 error", err)
	}
}
