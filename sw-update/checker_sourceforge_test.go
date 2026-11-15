package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func sfRSSBody(items []struct{ title, link string }) string {
	var out strings.Builder
	out.WriteString(`<?xml version="1.0"?><rss><channel>`)
	for _, it := range items {
		fmt.Fprintf(&out, "<item><title>%s</title><link>%s</link></item>", it.title, it.link)
	}
	out.WriteString(`</channel></rss>`)
	return out.String()
}

func TestSFChecker(t *testing.T) {
	app := App{
		Name: "testapp",
		Checker: CheckerConfig{
			Type:         "sourceforge",
			Project:      "myproject",
			VersionRegex: `(\d+\.\d+\.\d+)`,
		},
	}

	t.Run("picks highest version", func(t *testing.T) {
		body := sfRSSBody([]struct{ title, link string }{
			{"/myproject/1.9.0/app-1.9.0.zip", "https://sf.net/1.9.0"},
			{"/myproject/2.1.0/app-2.1.0.zip", "https://sf.net/2.1.0"},
			{"/myproject/2.0.0/app-2.0.0.zip", "https://sf.net/2.0.0"},
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		defer srv.Close()

		checker := &sfChecker{client: clientFor(srv)}
		result, err := checker.check(app)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Version != "2.1.0" {
			t.Errorf("version = %q, want %q", result.Version, "2.1.0")
		}
	})

	t.Run("no matching items returns error", func(t *testing.T) {
		body := sfRSSBody([]struct{ title, link string }{
			{"/myproject/readme.txt", "https://sf.net/readme"},
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		defer srv.Close()

		_, err := (&sfChecker{client: clientFor(srv)}).check(app)
		if err == nil {
			t.Error("expected error when no items match version_regex")
		}
	})

	t.Run("requires project", func(t *testing.T) {
		bad := app
		bad.Checker.Project = ""
		_, err := (&sfChecker{client: http.DefaultClient}).check(bad)
		if err == nil {
			t.Error("expected error for missing project")
		}
	})

	t.Run("requires version_regex", func(t *testing.T) {
		bad := app
		bad.Checker.VersionRegex = ""
		_, err := (&sfChecker{client: http.DefaultClient}).check(bad)
		if err == nil {
			t.Error("expected error for missing version_regex")
		}
	})

	t.Run("asset_patterns filters assets", func(t *testing.T) {
		body := sfRSSBody([]struct{ title, link string }{
			{"/myproject/1.0.0/app-1.0.0.exe", "https://sf.net/app.exe"},
			{"/myproject/1.0.0/app-1.0.0.zip", "https://sf.net/app.zip"},
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		defer srv.Close()

		appWithPattern := app
		appWithPattern.Checker.AssetPatterns = []string{"*.exe"}
		result, err := (&sfChecker{client: clientFor(srv)}).check(appWithPattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Assets) != 1 || result.Assets[0].Name != "app-1.0.0.exe" {
			t.Errorf("unexpected assets: %v", result.Assets)
		}
	})

	t.Run("same file in several folders keeps the shallowest", func(t *testing.T) {
		body := sfRSSBody([]struct{ title, link string }{
			{"/myproject/1.0.0/Windows/old_platforms/app-1.0.0-amd64.exe", "https://sf.net/old"},
			{"/myproject/1.0.0/Windows/app-1.0.0-win32.exe", "https://sf.net/win32"},
			{"/myproject/1.0.0/Windows/app-1.0.0-amd64.exe", "https://sf.net/current"},
			{"/myproject/1.0.0/Windows/legacy/deep/app-1.0.0-amd64.exe", "https://sf.net/deeper"},
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		defer srv.Close()

		appWithPattern := app
		appWithPattern.Checker.AssetPatterns = []string{`app-.*\.exe`}
		result, err := (&sfChecker{client: clientFor(srv)}).check(appWithPattern)
		if err != nil {
			t.Fatal(err)
		}
		want := []Asset{
			{Name: "app-1.0.0-amd64.exe", URL: "https://sf.net/current"},
			{Name: "app-1.0.0-win32.exe", URL: "https://sf.net/win32"},
		}
		if !slices.Equal(result.Assets, want) {
			t.Errorf("assets = %v, want %v", result.Assets, want)
		}
	})

	t.Run("non-200 returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "gone", http.StatusGone)
		}))
		defer srv.Close()
		_, err := (&sfChecker{client: clientFor(srv)}).check(app)
		if err == nil {
			t.Error("expected error for non-200 response")
		}
	})
}
