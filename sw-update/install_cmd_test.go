package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newInstallEnv gives cmdInstall a temp config dir holding one html-checked
// app, "app", whose page and assets h serves over TLS. The page offers a.zip
// and b.zip at version 1.0.
func newInstallEnv(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("APPDATA", root)         // os.UserConfigDir on Windows
	t.Setenv("XDG_CONFIG_HOME", root) // and on Linux

	srv := newDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, `version 1.0 <a href="/a.zip">a</a> <a href="/b.zip">b</a>`)
			return
		}
		h(w, r)
	}))
	prev := checkTransport
	checkTransport = srv.Client().Transport
	t.Cleanup(func() { checkTransport = prev })

	cfg := &Config{
		DownloadDir: filepath.Join(root, "downloads"),
		Apps: []App{{
			Name:    "app",
			Enabled: true,
			Checker: CheckerConfig{
				Type:          "html",
				URL:           srv.URL + "/",
				VersionRegex:  `version (\d+\.\d+)`,
				AssetPatterns: []string{"*.zip"},
			},
			Local: LocalConfig{Type: "none"},
		}},
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
}

// runInstall runs `sw-update install app`, answering its prompts with answers,
// and returns what it printed to stdout.
func runInstall(t *testing.T, answers string) (string, error) {
	t.Helper()
	in := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(in, []byte(answers), 0644); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevIn, prevOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdin, w
	defer func() { os.Stdin, os.Stdout = prevIn, prevOut }()

	out := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	installErr := cmdInstall([]string{"app"})
	w.Close()
	return <-out, installErr
}

// markedVersion returns the version state records as last seen for "app".
func markedVersion(t *testing.T) string {
	t.Helper()
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st := state["app"]; st != nil {
		return st.LastSeenVersion
	}
	return ""
}

// serveAssets serves a.zip, and b.zip unless bMissing.
func serveAssets(bMissing bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/b.zip" && bMissing {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("PK payload"))
	}
}

// Answers: proceed with download, don't run/open, then the mark prompt.
const proceedNoRun = "y\nn\n"

func TestInstallMarksVersionWhenAllAssetsDownload(t *testing.T) {
	newInstallEnv(t, serveAssets(false))

	out, err := runInstall(t, proceedNoRun+"\n")
	if err != nil {
		t.Fatalf("cmdInstall: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Mark version as installed/seen? [Y/n]") {
		t.Errorf("mark prompt should default to Yes:\n%s", out)
	}
	if got := markedVersion(t); got != "1.0" {
		t.Errorf("marked version = %q, want 1.0", got)
	}
}

func TestInstallFailsWhenAnAssetFails(t *testing.T) {
	newInstallEnv(t, serveAssets(true))

	out, err := runInstall(t, proceedNoRun+"\n")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 assets failed") {
		t.Errorf("err = %v, want 1 of 2 assets failed", err)
	}
	if !strings.Contains(out, "Mark version as installed/seen? [y/N]") {
		t.Errorf("mark prompt should default to No:\n%s", out)
	}
	if got := markedVersion(t); got != "" {
		t.Errorf("marked version = %q, want unmarked", got)
	}
}

func TestInstallMarksOnExplicitYesButStillFailsAfterAssetFailure(t *testing.T) {
	newInstallEnv(t, serveAssets(true))

	_, err := runInstall(t, proceedNoRun+"y\n")
	if err == nil {
		t.Error("cmdInstall succeeded, want an error for the failed asset")
	}
	if got := markedVersion(t); got != "1.0" {
		t.Errorf("marked version = %q, want 1.0", got)
	}
}
