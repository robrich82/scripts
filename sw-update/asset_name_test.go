package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAssetName(t *testing.T) {
	for _, name := range []string{"app-1.2.exe", "App Setup 1.2.msi", "tool_x64.7z"} {
		if err := checkAssetName(name); err != nil {
			t.Errorf("checkAssetName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{
		"", ".", "..",
		`..\evil.exe`, `x\..\..\evil.exe`, "../evil.exe", "a/b.exe",
		`C:\evil.exe`, "C:evil.exe", "app.exe:stream",
	} {
		if err := checkAssetName(name); err == nil {
			t.Errorf("checkAssetName(%q) = nil, want error", name)
		}
	}
}

// A link whose percent-encoded backslashes decode to a path must not be
// written outside the download directory.
func TestDownloadAssetRejectsEncodedTraversalFromHTMLPage(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write([]byte("payload"))
	}))
	defer srv.Close()

	page := []byte(`<a href="/files/app-x%5C..%5C..%5C..%5Cevil.exe">exe</a>`)
	assets := extractHTMLAssets(page, srv.URL+"/releases", []string{`app-.*\.exe`})
	if len(assets) != 1 {
		t.Fatalf("expected the crafted link to match, got %v", assets)
	}

	root := t.TempDir()
	destDir := filepath.Join(root, "downloads", "app")
	if _, err := downloadAsset(assets[0], destDir, ""); err == nil || !strings.Contains(err.Error(), "unsafe asset name") {
		t.Errorf("downloadAsset error = %v, want unsafe asset name", err)
	}
	if requests != 0 {
		t.Errorf("server got %d requests, want none", requests)
	}
	if _, err := os.Stat(filepath.Join(root, "evil.exe")); !os.IsNotExist(err) {
		t.Errorf("evil.exe written outside destDir (stat err %v)", err)
	}
}
