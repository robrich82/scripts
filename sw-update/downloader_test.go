package main

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadAssetSendsUserAgentAndReferer(t *testing.T) {
	var gotUA, gotReferer string
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if !strings.HasPrefix(gotUA, "sw-update/") {
		t.Errorf("User-Agent = %q, want sw-update/...", gotUA)
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
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestDownloadAssetFollowsMetaRefreshInterstitial(t *testing.T) {
	var fileReferer string
	mux := http.NewServeMux()
	mux.HandleFunc("/files/a.zip/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><meta http-equiv="refresh" content="5; url=/mirror/a.zip?ts=1&amp;r=x"></head></html>`))
	})
	mux.HandleFunc("/mirror/a.zip", func(w http.ResponseWriter, r *http.Request) {
		fileReferer = r.Header.Get("Referer")
		if r.URL.RawQuery != "ts=1&r=x" {
			t.Errorf("query = %q, want entity-decoded ts=1&r=x", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("PK"))
	})
	srv := newDownloadServer(t, mux)
	defer srv.Close()

	path, err := downloadAsset(Asset{Name: "a.zip", URL: srv.URL + "/files/a.zip/download"}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
	if body, _ := os.ReadFile(path); string(body) != "PK" {
		t.Errorf("body = %q, want the file behind the interstitial", body)
	}
	if want := srv.URL + "/files/a.zip/download"; fileReferer != want {
		t.Errorf("Referer = %q, want the interstitial %q", fileReferer, want)
	}
}

func TestDownloadAssetRejectsHTMLPageWithoutRefresh(t *testing.T) {
	calls := 0
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>please log in</html>"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := downloadAsset(Asset{Name: "a.zip", URL: srv.URL}, dir, "")
	var page *htmlPageError
	if !errors.As(err, &page) {
		t.Fatalf("err = %v, want an htmlPageError", err)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want 1 (no retry)", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.zip")); !os.IsNotExist(err) {
		t.Errorf("a.zip was written (stat err %v); the HTML page must not be saved as the asset", err)
	}
}

func TestDownloadAssetSavesBinaryMislabelledAsHTML(t *testing.T) {
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("PK\x03\x04binary"))
	}))
	defer srv.Close()

	path, err := downloadAsset(Asset{Name: "a.zip", URL: srv.URL}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
	if body, _ := os.ReadFile(path); string(body) != "PK\x03\x04binary" {
		t.Errorf("body = %q, want the binary saved as-is", body)
	}
}

func TestDownloadAssetGivesUpOnRefreshLoop(t *testing.T) {
	calls := 0
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><meta http-equiv="refresh" content="0; url=/again"></html>`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := downloadAsset(Asset{Name: "a.zip", URL: srv.URL + "/start"}, dir, "")
	var page *htmlPageError
	if !errors.As(err, &page) || page.Hops != 3 {
		t.Fatalf("err = %v, want an htmlPageError after 3 hops", err)
	}
	if page.URL != srv.URL+"/start" {
		t.Errorf("error URL = %q, want the asset URL", page.URL)
	}
	if calls != 4 {
		t.Errorf("server calls = %d, want 4 (the page plus 3 refreshes, no retry)", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.zip")); !os.IsNotExist(err) {
		t.Errorf("a.zip was written (stat err %v)", err)
	}
}

func TestDownloadAssetSavesHTMLAssetVerbatim(t *testing.T) {
	const page = `<html><meta http-equiv="refresh" content="0; url=/elsewhere"></html>`
	calls := 0
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	path, err := downloadAsset(Asset{Name: "notes.HTM", URL: srv.URL}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
	if body, _ := os.ReadFile(path); string(body) != page || calls != 1 {
		t.Errorf("body = %q after %d calls, want the page saved without following it", body, calls)
	}
}

func TestDownloadAssetInterstitialRefererAcrossHosts(t *testing.T) {
	var fileReferer string
	mirror := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileReferer = r.Header.Get("Referer")
		w.Header().Set("Content-Type", "application/zip")
		w.Write([]byte("PK"))
	}))
	defer mirror.Close()
	page := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><meta http-equiv="refresh" content="0; url=` + mirror.URL + `/a.zip"></html>`))
	}))
	defer page.Close()

	if _, err := downloadAsset(Asset{Name: "a.zip", URL: page.URL + "/download"}, t.TempDir(), ""); err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
	if want := mirror.URL + "/"; fileReferer != want {
		t.Errorf("Referer = %q, want the mirror's own origin %q", fileReferer, want)
	}
}

func TestMetaRefreshURL(t *testing.T) {
	base, _ := url.Parse("https://example.com/files/x/download")
	tests := []struct {
		name, page, want string
	}{
		{"absolute", `<meta http-equiv="refresh" content="5; url=https://dl.example.com/x.zip?a=1&amp;b=2">`, "https://dl.example.com/x.zip?a=1&b=2"},
		{"relative", `<meta http-equiv='Refresh' content='0;URL=/m/x.zip'>`, "https://example.com/m/x.zip"},
		{"content first", `<META content="0; url='x.zip'" HTTP-EQUIV="refresh">`, "https://example.com/files/x/x.zip"},
		{"bare ampersand", `<meta http-equiv="refresh" content="0; url=/x.zip?a=1&region=us&amp;b=2">`, "https://example.com/x.zip?a=1&region=us&b=2"},
		{"unquoted content", `<meta http-equiv=refresh content=0;url=/m/x.zip>`, "https://example.com/m/x.zip"},
		{"data-content before content", `<meta data-content="x" http-equiv="refresh" content="0; url=/m/x.zip">`, "https://example.com/m/x.zip"},
		{"non-http target", `<meta http-equiv="refresh" content="0; url=javascript:alert(1)">`, ""},
		{"no url", `<meta http-equiv="refresh" content="30">`, ""},
		{"other meta", `<meta name="description" content="url=/nope">`, ""},
		{"none", `<html></html>`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metaRefreshURL([]byte(tt.page), base); got != tt.want {
				t.Errorf("metaRefreshURL = %q, want %q", got, tt.want)
			}
		})
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
