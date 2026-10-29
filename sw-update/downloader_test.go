package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadAssetSendsBrowserHeaders(t *testing.T) {
	var gotUA, gotReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotReferer = r.Header.Get("Referer")
		w.Write([]byte("payload"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path, err := downloadAsset(Asset{Name: "a.zip", URL: srv.URL + "/a.zip"}, dir, srv.URL+"/page.html")
	if err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
	if gotUA != userAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, userAgent)
	}
	if want := srv.URL + "/page.html"; gotReferer != want {
		t.Errorf("Referer = %q, want %q", gotReferer, want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(body) != "payload" {
		t.Errorf("body = %q, want %q", body, "payload")
	}
	if got := filepath.Base(path); got != "a.zip" {
		t.Errorf("basename = %q, want a.zip", got)
	}
}

func TestDownloadAssetRetriesTransientStatus(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if _, err := downloadAsset(Asset{Name: "a.zip", URL: srv.URL}, t.TempDir(), ""); err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2", calls)
	}
}

func TestDownloadAssetGivesUpOnPermanentStatus(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := downloadAsset(Asset{Name: "a.zip", URL: srv.URL}, t.TempDir(), "")
	if err == nil {
		t.Fatal("expected an error for a 404")
	}
	var status *httpStatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusNotFound {
		t.Errorf("err = %v, want an httpStatusError with 404", err)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want 1 (no retry on 404)", calls)
	}
}

func TestDownloadReferer(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		referer string
		want    string
	}{
		{
			name:    "same host keeps the page referer",
			rawURL:  "https://falcosoft.hu/midiplayer_66_x64.zip",
			referer: "https://falcosoft.hu/softwares.html",
			want:    "https://falcosoft.hu/softwares.html",
		},
		{
			name:    "cross host falls back to the asset origin",
			rawURL:  "https://cdn.example.com/a.zip",
			referer: "https://falcosoft.hu/softwares.html",
			want:    "https://cdn.example.com/",
		},
		{
			name:   "no referer falls back to the asset origin",
			rawURL: "https://falcosoft.hu/a.zip",
			want:   "https://falcosoft.hu/",
		},
		{
			name:   "unparseable url yields no referer",
			rawURL: "not a url",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := downloadReferer(tt.rawURL, tt.referer); got != tt.want {
				t.Errorf("downloadReferer(%q, %q) = %q, want %q", tt.rawURL, tt.referer, got, tt.want)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{&httpStatusError{StatusCode: http.StatusForbidden}, true},
		{&httpStatusError{StatusCode: http.StatusTooManyRequests}, true},
		{&httpStatusError{StatusCode: http.StatusRequestTimeout}, true},
		{&httpStatusError{StatusCode: http.StatusBadGateway}, true},
		{&httpStatusError{StatusCode: http.StatusNotFound}, false},
		{&httpStatusError{StatusCode: http.StatusUnauthorized}, false},
		{errors.New("connection reset"), true},
	}
	for _, tt := range tests {
		if got := isRetryable(tt.err); got != tt.want {
			t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
