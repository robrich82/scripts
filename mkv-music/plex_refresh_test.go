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

// A PUT that a 301/302/303 would send on as a GET fails, unsent; a 307 keeps
// the method and is followed.
func TestHTTPPlexClient_RefreshMetadata_Redirects(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr bool
	}{
		{http.StatusMovedPermanently, true},
		{http.StatusFound, true},
		{http.StatusSeeOther, true},
		{http.StatusTemporaryRedirect, false},
	} {
		var reqs []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqs = append(reqs, r.Method+" "+r.URL.Path)
			if r.URL.Path != "/moved" {
				http.Redirect(w, r, "/moved", tc.status)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		err := newHTTPPlexClient(srv.URL, "token").RefreshMetadata("42")
		srv.Close()
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "refusing a redirect that would send PUT as GET") {
				t.Errorf("%d: err = %v; want a redirect error", tc.status, err)
			}
			if len(reqs) != 1 {
				t.Errorf("%d: requests %v; want only the first PUT", tc.status, reqs)
			}
			continue
		}
		if err != nil {
			t.Errorf("%d: err = %v", tc.status, err)
		}
		if len(reqs) != 2 || reqs[1] != "PUT /moved" {
			t.Errorf("%d: requests %v; want the PUT followed to /moved", tc.status, reqs)
		}
	}
}
