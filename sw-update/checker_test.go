package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// redirectTransport rewrites all requests to the given test server, preserving the path.
type redirectTransport struct {
	server *httptest.Server
}

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = r.server.Listener.Addr().String()
	return http.DefaultTransport.RoundTrip(clone)
}

func clientFor(srv *httptest.Server) *http.Client {
	return &http.Client{Transport: &redirectTransport{server: srv}}
}

// ---- matchAssetPattern ----

func TestMatchAssetPattern(t *testing.T) {
	tests := []struct {
		pat, name string
		want      bool
	}{
		// Glob (leading * causes regex compile failure, falls back to filepath.Match)
		{"*.exe", "setup.exe", true},
		{"*.exe", "setup.msi", false},
		{"*.zip", "app-1.2.zip", true},
		{"*.zip", "other-1.2.zip", true},
		// Glob that also compiles as a regex (where it wouldn't match)
		{"exiftool-*_64.zip", "exiftool-13.60_64.zip", true},
		{"exiftool-*_64.zip", "exiftool-13.60_32.zip", false},
		// Regex (valid regexp, used directly)
		{`app-\d+\.\d+\.exe`, "app-1.2.exe", true},
		{`app-\d+\.\d+\.exe`, "app-abc.exe", false},
		// Regex with alternation
		{`.*\.(exe|msi)`, "setup.exe", true},
		{`.*\.(exe|msi)`, "setup.dmg", false},
	}
	for _, tt := range tests {
		got := matchAssetPattern(tt.pat, tt.name)
		if got != tt.want {
			t.Errorf("matchAssetPattern(%q, %q) = %v, want %v", tt.pat, tt.name, got, tt.want)
		}
	}
}

// ---- isNonWindowsAsset ----

func TestIsNonWindowsAsset(t *testing.T) {
	yes := []string{
		"app.AppImage", "app.deb", "app.rpm", "app.dmg",
		"app-linux-x64.tar.gz", "app-macos-arm64.zip", "app-darwin-amd64.tar.gz",
		"app.tar.xz", "app-steamrt-sniper.tar.gz", "app-musl-static.tar.gz",
	}
	no := []string{
		"app-windows-x64.exe", "app-win32.zip", "setup.msi", "portable.7z", "app.zip",
	}
	for _, name := range yes {
		if !isNonWindowsAsset(name) {
			t.Errorf("isNonWindowsAsset(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if isNonWindowsAsset(name) {
			t.Errorf("isNonWindowsAsset(%q) = true, want false", name)
		}
	}
}

// ---- sortAndLimitAssets ----

func TestSortAndLimitAssets(t *testing.T) {
	assets := []Asset{
		{Name: "b.zip"}, {Name: "a.zip"}, {Name: "c.zip"},
	}

	t.Run("empty sort returns all", func(t *testing.T) {
		got := sortAndLimitAssets(assets, "")
		if len(got) != 3 {
			t.Fatalf("got %d assets, want 3", len(got))
		}
	})

	t.Run("name sort picks first alphabetically", func(t *testing.T) {
		got := sortAndLimitAssets(assets, "name")
		if len(got) != 1 || got[0].Name != "a.zip" {
			t.Errorf("got %v, want [{a.zip}]", got)
		}
	})

	t.Run("name_desc picks last alphabetically", func(t *testing.T) {
		got := sortAndLimitAssets(assets, "name_desc")
		if len(got) != 1 || got[0].Name != "c.zip" {
			t.Errorf("got %v, want [{c.zip}]", got)
		}
	})

	t.Run("single asset not affected by sort", func(t *testing.T) {
		single := []Asset{{Name: "only.zip"}}
		got := sortAndLimitAssets(single, "name")
		if len(got) != 1 || got[0].Name != "only.zip" {
			t.Errorf("unexpected result: %v", got)
		}
	})
}

// ---- autoSelectWindows ----

func TestAutoSelectWindows(t *testing.T) {
	t.Run("prefers windows keyword over no keyword", func(t *testing.T) {
		assets := []ghAsset{
			{Name: "app-linux.tar.gz", BrowserDownloadURL: "u1"},
			{Name: "app-windows-x64.zip", BrowserDownloadURL: "u2"},
			{Name: "app-source.zip", BrowserDownloadURL: "u3"},
		}
		got := autoSelectWindows(assets)
		if len(got) != 1 || got[0].Name != "app-windows-x64.zip" {
			t.Errorf("unexpected result: %v", got)
		}
	})

	t.Run("prefers exe/msi/zip over other", func(t *testing.T) {
		assets := []ghAsset{
			{Name: "app.tar.gz", BrowserDownloadURL: "u1"},
			{Name: "setup.exe", BrowserDownloadURL: "u2"},
		}
		got := autoSelectWindows(assets)
		if len(got) != 1 || got[0].Name != "setup.exe" {
			t.Errorf("unexpected result: %v", got)
		}
	})

	t.Run("falls back to all when all are non-windows", func(t *testing.T) {
		assets := []ghAsset{
			{Name: "app.AppImage", BrowserDownloadURL: "u1"},
			{Name: "app.deb", BrowserDownloadURL: "u2"},
		}
		got := autoSelectWindows(assets)
		if len(got) != 2 {
			t.Errorf("expected 2 fallback assets, got %d", len(got))
		}
	})
}

// ---- extractHTMLAssets ----

func TestExtractHTMLAssets(t *testing.T) {
	body := []byte(`<html>
<a href="/downloads/app-1.2.exe">exe</a>
<a href="/downloads/app-1.2.msi">msi</a>
<a href="/downloads/README.txt">txt</a>
<a href="/downloads/app-1.2.exe">exe duplicate</a>
</html>`)
	pageURL := "https://example.com/releases"

	t.Run("no patterns returns nil", func(t *testing.T) {
		got := extractHTMLAssets(body, pageURL, nil)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("glob pattern matches and resolves URL", func(t *testing.T) {
		got := extractHTMLAssets(body, pageURL, []string{"*.exe"})
		if len(got) != 1 {
			t.Fatalf("expected 1 asset, got %d: %v", len(got), got)
		}
		if got[0].Name != "app-1.2.exe" {
			t.Errorf("unexpected name: %q", got[0].Name)
		}
		if got[0].URL != "https://example.com/downloads/app-1.2.exe" {
			t.Errorf("unexpected URL: %q", got[0].URL)
		}
	})

	t.Run("multiple patterns", func(t *testing.T) {
		got := extractHTMLAssets(body, pageURL, []string{"*.exe", "*.msi"})
		if len(got) != 2 {
			t.Errorf("expected 2 assets, got %d: %v", len(got), got)
		}
	})

	t.Run("deduplicates URLs", func(t *testing.T) {
		got := extractHTMLAssets(body, pageURL, []string{"*.exe"})
		if len(got) != 1 {
			t.Errorf("expected dedup to 1, got %d", len(got))
		}
	})

	t.Run("matches single-quoted href attributes", func(t *testing.T) {
		singleQuoted := []byte(`<a href='/downloads/app-1.2.exe'>exe</a>`)
		got := extractHTMLAssets(singleQuoted, pageURL, []string{"*.exe"})
		if len(got) != 1 {
			t.Fatalf("expected 1 asset, got %d: %v", len(got), got)
		}
		if got[0].Name != "app-1.2.exe" {
			t.Errorf("unexpected name: %q", got[0].Name)
		}
		if got[0].URL != "https://example.com/downloads/app-1.2.exe" {
			t.Errorf("unexpected URL: %q", got[0].URL)
		}
	})

	t.Run("names SourceForge /download links by the file segment", func(t *testing.T) {
		const href = "https://sourceforge.net/projects/exiftool/files/exiftool-13.60_64.zip/download"
		sf := []byte(`<a href="` + href + `">zip</a>`)
		got := extractHTMLAssets(sf, pageURL, []string{"exiftool-*_64.zip"})
		if len(got) != 1 {
			t.Fatalf("expected 1 asset, got %d: %v", len(got), got)
		}
		if got[0].Name != "exiftool-13.60_64.zip" {
			t.Errorf("unexpected name: %q", got[0].Name)
		}
		if got[0].URL != href {
			t.Errorf("unexpected URL: %q", got[0].URL)
		}
	})
}

// ---- githubChecker ----

func TestGithubChecker(t *testing.T) {
	releases := []ghRelease{
		{
			TagName:    "v2.0.0",
			Prerelease: false,
			Draft:      false,
			Assets: []ghAsset{
				{Name: "app-windows-x64.zip", BrowserDownloadURL: "https://example.com/app-windows-x64.zip"},
			},
		},
		{
			TagName:    "v1.9.0",
			Prerelease: false,
			Draft:      false,
			Assets:     []ghAsset{{Name: "app-linux.tar.gz", BrowserDownloadURL: "https://example.com/app-linux.tar.gz"}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(releases)
	}))
	defer srv.Close()

	app := App{
		Name: "testapp",
		Checker: CheckerConfig{
			Type: "github",
			Repo: "owner/testapp",
		},
	}

	checker := &githubChecker{client: clientFor(srv)}
	result, err := checker.check(app)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Version != "2.0.0" {
		t.Errorf("version = %q, want %q", result.Version, "2.0.0")
	}

	t.Run("skips drafts", func(t *testing.T) {
		drafts := []ghRelease{
			{TagName: "v3.0.0", Draft: true},
			{TagName: "v2.0.0", Draft: false, Assets: []ghAsset{{Name: "app.exe"}}},
		}
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(drafts)
		}))
		defer srv2.Close()
		r, err := (&githubChecker{client: clientFor(srv2)}).check(app)
		if err != nil {
			t.Fatal(err)
		}
		if r.Version != "2.0.0" {
			t.Errorf("version = %q, want %q", r.Version, "2.0.0")
		}
	})

	t.Run("skips prereleases unless configured", func(t *testing.T) {
		pres := []ghRelease{
			{TagName: "v3.0.0-beta", Prerelease: true, Assets: []ghAsset{{Name: "app.exe"}}},
			{TagName: "v2.0.0", Assets: []ghAsset{{Name: "app.exe"}}},
		}
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(pres)
		}))
		defer srv2.Close()
		r, err := (&githubChecker{client: clientFor(srv2)}).check(app)
		if err != nil {
			t.Fatal(err)
		}
		if r.Version != "2.0.0" {
			t.Errorf("version = %q, want %q", r.Version, "2.0.0")
		}
	})

	t.Run("require_asset skips releases with no matching asset", func(t *testing.T) {
		rels := []ghRelease{
			{TagName: "v3.0.0", Assets: []ghAsset{{Name: "app-linux.tar.gz"}}},
			{TagName: "v2.0.0", Assets: []ghAsset{{Name: "app-windows.exe"}}},
		}
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(rels)
		}))
		defer srv2.Close()
		appWithAsset := App{
			Name: "testapp",
			Checker: CheckerConfig{
				Type:          "github",
				Repo:          "owner/testapp",
				AssetPatterns: []string{"*.exe"},
				RequireAsset:  true,
			},
		}
		r, err := (&githubChecker{client: clientFor(srv2)}).check(appWithAsset)
		if err != nil {
			t.Fatal(err)
		}
		if r.Version != "2.0.0" {
			t.Errorf("version = %q, want %q", r.Version, "2.0.0")
		}
	})

	t.Run("version_regex extracts version from asset name", func(t *testing.T) {
		// Releases tagged by date, with the real version in the asset filename.
		rels := []ghRelease{
			{TagName: "251105T2353", Prerelease: true, Assets: []ghAsset{
				{Name: "hakuneko-electron-v39.1.0-win32-x64.zip", BrowserDownloadURL: "https://example.com/x64.zip"},
				{Name: "hakuneko-electron-v39.1.0-win32-ia32.zip", BrowserDownloadURL: "https://example.com/ia32.zip"},
			}},
		}
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(rels)
		}))
		defer srv2.Close()
		appAsset := App{
			Name: "haruneko",
			Checker: CheckerConfig{
				Type:          "github",
				Repo:          "owner/haruneko",
				Prerelease:    true,
				AssetPatterns: []string{`hakuneko-electron-.*-win32-x64\.zip`},
				VersionRegex:  `hakuneko-electron-v([0-9.]+)-win32-x64\.zip`,
			},
		}
		r, err := (&githubChecker{client: clientFor(srv2)}).check(appAsset)
		if err != nil {
			t.Fatal(err)
		}
		if r.Version != "39.1.0" {
			t.Errorf("version = %q, want %q", r.Version, "39.1.0")
		}
	})

	t.Run("no releases returns error", func(t *testing.T) {
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]ghRelease{})
		}))
		defer srv2.Close()
		_, err := (&githubChecker{client: clientFor(srv2)}).check(app)
		if err == nil {
			t.Error("expected error for empty releases")
		}
	})

	t.Run("non-200 response returns error", func(t *testing.T) {
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rate limited", http.StatusForbidden)
		}))
		defer srv2.Close()
		_, err := (&githubChecker{client: clientFor(srv2)}).check(app)
		if err == nil {
			t.Error("expected error for non-200 response")
		}
	})
}

// ---- htmlChecker ----

func TestHTMLChecker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>
<title>Downloads</title>
<p>Current version: 3.14.1</p>
<a href="/files/app-3.14.1-setup.exe">Download</a>
<a href="/files/app-3.14.1.zip">Zip</a>
</html>`)
	}))
	defer srv.Close()

	app := App{
		Name: "testapp",
		Checker: CheckerConfig{
			Type:         "html",
			URL:          srv.URL + "/",
			VersionRegex: `Current version: (\d+\.\d+\.\d+)`,
		},
	}

	checker := &htmlChecker{client: srv.Client()}
	result, err := checker.check(app)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Version != "3.14.1" {
		t.Errorf("version = %q, want %q", result.Version, "3.14.1")
	}

	t.Run("asset_patterns extracts links", func(t *testing.T) {
		appWithAssets := app
		appWithAssets.Checker.AssetPatterns = []string{"*.exe"}
		r, err := checker.check(appWithAssets)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Assets) != 1 || r.Assets[0].Name != "app-3.14.1-setup.exe" {
			t.Errorf("unexpected assets: %v", r.Assets)
		}
	})

	t.Run("asset_page scrapes a separate download page", func(t *testing.T) {
		verSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/release-version":
				fmt.Fprint(w, "9.0.1")
			default:
				fmt.Fprint(w, `<a href="tool-release-full.7z">full</a>`)
			}
		}))
		defer verSrv.Close()

		split := App{
			Name: "testapp",
			Checker: CheckerConfig{
				Type:          "html",
				URL:           verSrv.URL + "/release-version",
				AssetPage:     verSrv.URL + "/builds/",
				VersionRegex:  `(\d+\.\d+\.\d+)`,
				AssetPatterns: []string{"tool-release-full.7z"},
			},
		}
		r, err := (&htmlChecker{client: verSrv.Client()}).check(split)
		if err != nil {
			t.Fatal(err)
		}
		if r.Version != "9.0.1" {
			t.Errorf("version = %q, want %q", r.Version, "9.0.1")
		}
		want := verSrv.URL + "/builds/tool-release-full.7z"
		if len(r.Assets) != 1 || r.Assets[0].URL != want {
			t.Errorf("assets = %v, want one asset at %s", r.Assets, want)
		}
	})

	t.Run("requires url", func(t *testing.T) {
		bad := app
		bad.Checker.URL = ""
		_, err := checker.check(bad)
		if err == nil {
			t.Error("expected error for missing url")
		}
	})

	t.Run("requires version_regex", func(t *testing.T) {
		bad := app
		bad.Checker.VersionRegex = ""
		_, err := checker.check(bad)
		if err == nil {
			t.Error("expected error for missing version_regex")
		}
	})

	t.Run("no match returns error", func(t *testing.T) {
		bad := app
		bad.Checker.VersionRegex = `NOMATCH-\d+`
		_, err := checker.check(bad)
		if err == nil {
			t.Error("expected error when regex doesn't match")
		}
	})

	t.Run("non-200 reports the status, not a regex miss", func(t *testing.T) {
		errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "origin unreachable", 525)
		}))
		defer errSrv.Close()

		down := app
		down.Checker.URL = errSrv.URL + "/"
		_, err := (&htmlChecker{client: errSrv.Client()}).check(down)
		if err == nil {
			t.Fatal("expected error for HTTP 525")
		}
		if !strings.Contains(err.Error(), "525") {
			t.Errorf("error = %v, want it to mention the 525 status", err)
		}
	})
}

// ---- headerChecker ----

func TestHeaderChecker(t *testing.T) {
	t.Run("Last-Modified parsed to date", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Last-Modified", "Wed, 01 Jan 2025 12:00:00 GMT")
		}))
		defer srv.Close()

		app := App{
			Name: "testapp",
			Checker: CheckerConfig{
				Type: "http-header",
				URL:  srv.URL + "/resource",
			},
		}
		result, err := (&headerChecker{client: srv.Client()}).check(app)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Version != "2025-01-01" {
			t.Errorf("version = %q, want %q", result.Version, "2025-01-01")
		}
	})

	t.Run("custom header returned as-is", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-App-Version", "4.2.0")
		}))
		defer srv.Close()

		app := App{
			Name: "testapp",
			Checker: CheckerConfig{
				Type:   "http-header",
				URL:    srv.URL + "/resource",
				Header: "X-App-Version",
			},
		}
		result, err := (&headerChecker{client: srv.Client()}).check(app)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Version != "4.2.0" {
			t.Errorf("version = %q, want %q", result.Version, "4.2.0")
		}
	})

	t.Run("missing header returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()

		app := App{
			Name:    "testapp",
			Checker: CheckerConfig{Type: "http-header", URL: srv.URL + "/resource"},
		}
		_, err := (&headerChecker{client: srv.Client()}).check(app)
		if err == nil {
			t.Error("expected error for missing Last-Modified header")
		}
	})

	t.Run("requires url", func(t *testing.T) {
		app := App{Name: "testapp", Checker: CheckerConfig{Type: "http-header"}}
		_, err := (&headerChecker{client: http.DefaultClient}).check(app)
		if err == nil {
			t.Error("expected error for missing url")
		}
	})

	t.Run("non-200 returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		app := App{
			Name:    "testapp",
			Checker: CheckerConfig{Type: "http-header", URL: srv.URL + "/resource"},
		}
		_, err := (&headerChecker{client: srv.Client()}).check(app)
		if err == nil {
			t.Error("expected error for 404 response")
		}
	})
}

// ---- checkerDispatch ----

func TestCheckerDispatch(t *testing.T) {
	t.Run("unknown type returns error", func(t *testing.T) {
		app := App{Name: "x", Checker: CheckerConfig{Type: "bogus"}}
		_, err := NewChecker(http.DefaultClient).Check(app)
		if err == nil {
			t.Error("expected error for unknown checker type")
		}
	})

	t.Run("empty type returns error", func(t *testing.T) {
		app := App{Name: "x", Checker: CheckerConfig{Type: ""}}
		_, err := NewChecker(http.DefaultClient).Check(app)
		if err == nil {
			t.Error("expected error for empty checker type")
		}
	})
}
