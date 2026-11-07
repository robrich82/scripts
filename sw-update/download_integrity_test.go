package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newDownloadServer starts a TLS test server and points downloadAsset at a
// transport that trusts it, since downloads must be https.
func newDownloadServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	prev := downloadTransport
	downloadTransport = srv.Client().Transport
	t.Cleanup(func() { downloadTransport = prev })
	return srv
}

// plainServer is an http:// server that counts the requests it gets.
func plainServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.Write([]byte("MZ payload"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertInsecure(t *testing.T, err error, dir string, plainCalls int) {
	t.Helper()
	var insecure *insecureURLError
	if !errors.As(err, &insecure) {
		t.Errorf("err = %v, want an insecureURLError", err)
	}
	if plainCalls != 0 {
		t.Errorf("plain-http server got %d requests, want none", plainCalls)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.exe")); !os.IsNotExist(err) {
		t.Errorf("a.exe was written (stat err %v)", err)
	}
}

func TestDownloadAssetRejectsPlainHTTP(t *testing.T) {
	calls := 0
	srv := plainServer(t, &calls)
	dir := t.TempDir()
	_, err := downloadAsset(Asset{Name: "a.exe", URL: srv.URL + "/a.exe"}, dir, "")
	assertInsecure(t, err, dir, calls)
}

func TestDownloadAssetRejectsRedirectToHTTP(t *testing.T) {
	plainCalls, tlsCalls := 0, 0
	plain := plainServer(t, &plainCalls)
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tlsCalls++
		http.Redirect(w, r, plain.URL+"/a.exe", http.StatusFound)
	}))
	dir := t.TempDir()
	_, err := downloadAsset(Asset{Name: "a.exe", URL: srv.URL + "/a.exe"}, dir, "")
	assertInsecure(t, err, dir, plainCalls)
	if tlsCalls != 1 {
		t.Errorf("https server got %d requests, want 1 (no retry)", tlsCalls)
	}
}

func TestDownloadAssetRejectsRefreshToHTTP(t *testing.T) {
	plainCalls := 0
	plain := plainServer(t, &plainCalls)
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><meta http-equiv="refresh" content="0; url=` + plain.URL + `/a.exe"></html>`))
	}))
	dir := t.TempDir()
	_, err := downloadAsset(Asset{Name: "a.exe", URL: srv.URL + "/download"}, dir, "")
	assertInsecure(t, err, dir, plainCalls)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// A GitHub release's asset digest travels through the checker's asset
// selection into downloadAsset, which accepts matching content and deletes
// a mismatching download without retrying.
func TestDownloadAssetVerifiesGitHubDigest(t *testing.T) {
	const payload = "MZ real installer"
	served := payload
	calls := 0
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(served))
	}))

	release := `{"tag_name":"v1.0","assets":[
		{"name":"app-win-x64.exe","browser_download_url":"` + srv.URL + `/app-win-x64.exe","digest":"sha256:` + sha256Hex([]byte(payload)) + `"},
		{"name":"app-win-x64.msi","browser_download_url":"` + srv.URL + `/app-win-x64.msi","digest":"sha512:abc"},
		{"name":"app-win-x64.zip","browser_download_url":"` + srv.URL + `/app-win-x64.zip"}]}`
	var rel ghRelease
	if err := json.Unmarshal([]byte(release), &rel); err != nil {
		t.Fatal(err)
	}
	assets := selectAssets(rel.Assets, []string{"*.exe", "*.msi", "*.zip"})
	if len(assets) != 3 {
		t.Fatalf("selectAssets = %v, want 3 assets", assets)
	}
	if assets[0].SHA256 != sha256Hex([]byte(payload)) {
		t.Errorf("exe SHA256 = %q, want the release digest", assets[0].SHA256)
	}
	if assets[1].SHA256 != "" || assets[2].SHA256 != "" {
		t.Errorf("non-sha256 or missing digests gave SHA256 %q, %q; want empty", assets[1].SHA256, assets[2].SHA256)
	}

	t.Run("matching content is saved", func(t *testing.T) {
		path, err := downloadAsset(assets[0], t.TempDir(), "")
		if err != nil {
			t.Fatalf("downloadAsset: %v", err)
		}
		if body, _ := os.ReadFile(path); string(body) != payload {
			t.Errorf("body = %q, want %q", body, payload)
		}
	})

	t.Run("tampered content is rejected and removed", func(t *testing.T) {
		served, calls = "MZ tampered", 0
		dir := t.TempDir()
		_, err := downloadAsset(assets[0], dir, "")
		var mismatch *sha256MismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("err = %v, want a sha256MismatchError", err)
		}
		if calls != 1 {
			t.Errorf("server calls = %d, want 1 (no retry)", calls)
		}
		if _, err := os.Stat(filepath.Join(dir, assets[0].Name)); !os.IsNotExist(err) {
			t.Errorf("tampered file left on disk (stat err %v)", err)
		}
		if _, err := os.Stat(filepath.Join(dir, assets[0].Name+".part")); !os.IsNotExist(err) {
			t.Errorf("tampered .part left on disk (stat err %v)", err)
		}
	})

	t.Run("assets without a digest are not checked", func(t *testing.T) {
		if _, err := downloadAsset(assets[2], t.TempDir(), ""); err != nil {
			t.Fatalf("downloadAsset: %v", err)
		}
	})
}

// The default path (no asset patterns) keeps the release digest too.
func TestAutoSelectWindowsCarriesDigest(t *testing.T) {
	got := autoSelectWindows([]ghAsset{
		{Name: "app-linux.tar.gz", BrowserDownloadURL: "u1", Digest: "sha256:aaa"},
		{Name: "app-windows-x64.exe", BrowserDownloadURL: "u2", Digest: "sha256:bbb"},
	})
	if len(got) != 1 || got[0].SHA256 != "bbb" {
		t.Errorf("autoSelectWindows = %+v, want app-windows-x64.exe with SHA256 bbb", got)
	}
}

func TestDownloadAssetAcceptsUppercaseDigest(t *testing.T) {
	const payload = "MZ real installer"
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	want := strings.ToUpper(sha256Hex([]byte(payload)))
	if _, err := downloadAsset(Asset{Name: "a.exe", URL: srv.URL + "/a.exe", SHA256: want}, t.TempDir(), ""); err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
}

// A body cut short leaves neither a partial file nor a .part behind, and an
// earlier good download under the same name survives every retry.
func TestDownloadAssetInterruptedKeepsEarlierCopy(t *testing.T) {
	prev := retryBackoff
	retryBackoff = time.Millisecond
	t.Cleanup(func() { retryBackoff = prev })

	calls := 0
	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("MZ trunc"))
	}))
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.exe")
	if err := os.WriteFile(dest, []byte("MZ good"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := downloadAsset(Asset{Name: "a.exe", URL: srv.URL + "/a.exe"}, dir, ""); err == nil {
		t.Fatal("downloadAsset succeeded on a truncated body")
	}
	if calls != downloadAttempts {
		t.Errorf("server calls = %d, want %d (retried)", calls, downloadAttempts)
	}
	if body, _ := os.ReadFile(dest); string(body) != "MZ good" {
		t.Errorf("earlier download = %q, want it untouched", body)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part left on disk (stat err %v)", err)
	}
}
