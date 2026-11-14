package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

func TestCheckerRefusesHTTPCheckerPage(t *testing.T) {
	calls := 0
	plain := plainServer(t, &calls)
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `version 2.5`)
	})

	pageApp := htmlApp(plain.URL + "/")
	assetPageApp := htmlApp(srv.URL + "/")
	assetPageApp.Checker.AssetPage = plain.URL + "/files/"
	headerApp := App{Name: "testapp", Checker: CheckerConfig{Type: "http-header", URL: plain.URL + "/app.exe"}}

	for name, app := range map[string]App{"url": pageApp, "asset_page": assetPageApp, "http-header": headerApp} {
		t.Run(name, func(t *testing.T) {
			_, err := NewChecker(srv.Client()).Check(app)
			var insecure *insecureURLError
			if !errors.As(err, &insecure) {
				t.Errorf("err = %v, want an insecureURLError", err)
			}
		})
	}
	if calls != 0 {
		t.Errorf("plain-http server got %d requests, want none", calls)
	}
}

func TestHeaderCheckerRefusesRedirectToHTTP(t *testing.T) {
	calls := 0
	plain := plainServer(t, &calls)
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/app.exe", http.StatusFound)
	})

	app := App{Name: "testapp", Checker: CheckerConfig{Type: "http-header", URL: srv.URL + "/app.exe"}}
	_, err := NewChecker(srv.Client()).Check(app)
	var insecure *insecureURLError
	if !errors.As(err, &insecure) {
		t.Errorf("err = %v, want an insecureURLError", err)
	}
	if calls != 0 {
		t.Errorf("plain-http server got %d requests, want none", calls)
	}
}

// hostTransport sends every request to srv, keeping its https scheme and path.
type hostTransport struct {
	srv *httptest.Server
}

func (h hostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Host = h.srv.Listener.Addr().String()
	return h.srv.Client().Transport.RoundTrip(clone)
}

// The asset check sits in dispatch, so it covers checkers other than html.
func TestCheckerRejectsHTTPAssetFromSourceForge(t *testing.T) {
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sfRSSBody([]struct{ title, link string }{
			{"/myproject/2.0.0/app-2.0.0.exe", "https://sf.net/app.exe"},
			{"/myproject/2.0.0/app-2.0.0.zip", "http://sf.net/app.zip"},
		}))
	})
	app := App{
		Name: "testapp",
		Checker: CheckerConfig{
			Type:          "sourceforge",
			Project:       "myproject",
			VersionRegex:  `(\d+\.\d+\.\d+)`,
			AssetPatterns: []string{"*.exe", "*.zip"},
		},
	}

	result, err := NewChecker(&http.Client{Transport: hostTransport{srv}}).Check(app)
	var insecure *insecureURLError
	if !errors.As(err, &insecure) || insecure.URL != "http://sf.net/app.zip" {
		t.Errorf("err = %v, result = %v, want an insecureURLError for the zip", err, result)
	}
}

func TestHTMLCheckerSendsUserAgent(t *testing.T) {
	var gotUA string
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		fmt.Fprint(w, `version 1.2 <a href="/app-1.2.exe">x</a>`)
	})

	res, err := NewChecker(srv.Client()).Check(htmlApp(srv.URL + "/downloads"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if gotUA != userAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, userAgent)
	}
	// amd.com's Akamai front end resets requests whose UA has a "(...)" comment.
	if strings.ContainsAny(gotUA, "()") {
		t.Errorf("User-Agent = %q, want no (...) comment", gotUA)
	}
	if res.Version != "1.2" {
		t.Errorf("Version = %q, want 1.2", res.Version)
	}
}

func TestCheckerReplacesAssetsWithTemplatedAsset(t *testing.T) {
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `version 6.0 <a href="https://mirror.example.com/app-6.0-setup.exe">x</a>`)
	})
	app := htmlApp(srv.URL + "/downloads") // asset_patterns "*.exe" matches the page link too
	app.Checker.AssetURL = "https://dl.example.com/app-{major}-{minor}/app-{version}-setup.exe"

	res, err := NewChecker(srv.Client()).Check(app)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := []Asset{{Name: "app-6.0-setup.exe", URL: "https://dl.example.com/app-6-0/app-6.0-setup.exe"}}
	if !reflect.DeepEqual(res.Assets, want) {
		t.Errorf("Assets = %+v, want only the templated %+v", res.Assets, want)
	}
}

func TestHeaderCheckerTemplatedAsset(t *testing.T) {
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Sat, 14 Nov 2026 10:00:00 GMT")
	})
	app := App{Name: "testapp", Checker: CheckerConfig{
		Type: "http-header", URL: srv.URL + "/latest", AssetURL: "https://dl.example.com/app-{version}.zip",
	}}

	res, err := NewChecker(srv.Client()).Check(app)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := []Asset{{Name: "app-2026-11-14.zip", URL: "https://dl.example.com/app-2026-11-14.zip"}}
	if !reflect.DeepEqual(res.Assets, want) {
		t.Errorf("Assets = %+v, want %+v", res.Assets, want)
	}
}

func TestCheckerRejectsHTTPTemplatedAsset(t *testing.T) {
	srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `version 6.0`)
	})
	app := htmlApp(srv.URL + "/downloads")
	app.Checker.AssetURL = "http://dl.example.com/app-{version}.exe"

	_, err := NewChecker(srv.Client()).Check(app)
	var insecure *insecureURLError
	if !errors.As(err, &insecure) {
		t.Errorf("err = %v, want an insecureURLError", err)
	}
}

func TestCheckerFailsOnBadAssetURL(t *testing.T) {
	tests := []struct {
		name, page, versionRegex, assetURL, wantErr string
	}{
		{"missing segment", "version 6.0", `version (\d+\.\d+)`, "https://dl.example.com/app-{patch}.zip", "{patch}"},
		{"empty version", "version  ", `version ([0-9.]*)`, "https://dl.example.com/app-{version}.zip", "no version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newPageServer(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.page)
			})
			app := htmlApp(srv.URL + "/downloads")
			app.Checker.VersionRegex = tt.versionRegex
			app.Checker.AssetURL = tt.assetURL

			res, err := NewChecker(srv.Client()).Check(app)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
			if res != nil {
				t.Errorf("result = %+v, want nil", res)
			}
		})
	}
}
