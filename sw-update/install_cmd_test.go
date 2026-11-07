package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newInstallEnv gives cmdInstall a temp config dir holding one html-checked
// app, "app", with the given local detector type, whose page and assets h
// serves over TLS. The page offers a.zip and b.zip at version 1.0.
func newInstallEnv(t *testing.T, localType string, h http.HandlerFunc) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("APPDATA", root)         // os.UserConfigDir on Windows
	t.Setenv("XDG_CONFIG_HOME", root) // on Linux
	t.Setenv("HOME", root)            // and on macOS

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
			Local: LocalConfig{Type: localType},
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
	defer r.Close()
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
	newInstallEnv(t, "none", serveAssets(false))

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
	newInstallEnv(t, "none", serveAssets(true))

	out, err := runInstall(t, proceedNoRun+"\n")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 assets failed to download: b.zip") {
		t.Errorf("err = %v, want 1 of 2 assets failed to download: b.zip", err)
	}
	if !strings.Contains(out, "Not downloaded: b.zip") {
		t.Errorf("no note of the failed asset before the run prompt:\n%s", out)
	}
	if !strings.Contains(out, "Mark version as installed/seen? [y/N]") {
		t.Errorf("mark prompt should default to No:\n%s", out)
	}
	if got := markedVersion(t); got != "" {
		t.Errorf("marked version = %q, want unmarked", got)
	}
}

func TestInstallMarksOnExplicitYesButStillFailsAfterAssetFailure(t *testing.T) {
	newInstallEnv(t, "none", serveAssets(true))

	_, err := runInstall(t, proceedNoRun+"y\n")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 assets failed") {
		t.Errorf("err = %v, want 1 of 2 assets failed", err)
	}
	if got := markedVersion(t); got != "1.0" {
		t.Errorf("marked version = %q, want 1.0", got)
	}
}

// An app with a local detector gets no mark prompt, but still fails.
func TestInstallWithLocalDetectorFailsWhenAnAssetFails(t *testing.T) {
	newInstallEnv(t, "exe", serveAssets(true))

	out, err := runInstall(t, proceedNoRun)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 assets failed") {
		t.Errorf("err = %v, want 1 of 2 assets failed", err)
	}
	if strings.Contains(out, "Mark version") {
		t.Errorf("asked to mark an app with a local detector:\n%s", out)
	}
}

// writeStateFile replaces the state file's contents with data.
func writeStateFile(t *testing.T, data string, perm os.FileMode) string {
	t.Helper()
	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sw-update-state.json")
	if err := os.WriteFile(path, []byte(data), perm); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallReportsStateLoadError(t *testing.T) {
	newInstallEnv(t, "none", serveAssets(false))
	writeStateFile(t, "{not json", 0644)

	out, err := runInstall(t, proceedNoRun+"\n")
	if err == nil || !strings.Contains(err.Error(), "load state") {
		t.Errorf("err = %v, want a load state error", err)
	}
	if strings.Contains(out, "Marked") {
		t.Errorf("printed Marked although the state could not be loaded:\n%s", out)
	}
}

func TestInstallReportsStateSaveError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("relies on Windows refusing to replace a read-only file")
	}
	newInstallEnv(t, "none", serveAssets(false))
	path := writeStateFile(t, "{}", 0444)
	t.Cleanup(func() { os.Chmod(path, 0644) })

	out, err := runInstall(t, proceedNoRun+"\n")
	if err == nil || !strings.Contains(err.Error(), "save state") {
		t.Errorf("err = %v, want a save state error", err)
	}
	if strings.Contains(out, "Marked") {
		t.Errorf("printed Marked although the state was not saved:\n%s", out)
	}
}

// Answering yes at Run/open extracts the served zips, which are not valid
// archives, so every run/extract fails without launching anything.
const proceedRun = "y\ny\n"

func TestInstallFailsWhenExtractFails(t *testing.T) {
	newInstallEnv(t, "none", serveAssets(false))

	out, err := runInstall(t, proceedRun+"\n")
	if err == nil || !strings.Contains(err.Error(), "2 of 2 files failed to run or extract: a.zip, b.zip") {
		t.Errorf("err = %v, want 2 of 2 files failed to run or extract: a.zip, b.zip", err)
	}
	if !strings.Contains(out, "Mark version as installed/seen? [y/N]") {
		t.Errorf("mark prompt should default to No:\n%s", out)
	}
	if got := markedVersion(t); got != "" {
		t.Errorf("marked version = %q, want unmarked", got)
	}
}

func TestInstallReportsDownloadAndExtractFailures(t *testing.T) {
	newInstallEnv(t, "exe", serveAssets(true))

	_, err := runInstall(t, proceedRun)
	if err == nil {
		t.Fatal("cmdInstall succeeded, want download and extract errors")
	}
	for _, want := range []string{
		"1 of 2 assets failed to download: b.zip",
		"1 of 1 files failed to run or extract: a.zip",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to include %q", err, want)
		}
	}
}

// Input that ends after confirming the download must not run anything: the
// served zips aren't valid archives, so an attempt would fail the install.
func TestInstallEOFAtRunPromptDoesNotRun(t *testing.T) {
	newInstallEnv(t, "none", serveAssets(false))

	out, err := runInstall(t, "y\n")
	if err != nil {
		t.Fatalf("cmdInstall: %v\n%s", err, out)
	}
	if got := markedVersion(t); got != "" {
		t.Errorf("marked version = %q, want unmarked at end of input", got)
	}
}

func TestInstallNoAtRunPromptDoesNotRun(t *testing.T) {
	newInstallEnv(t, "none", serveAssets(false))

	out, err := runInstall(t, "y\nNo\n\n")
	if err != nil {
		t.Fatalf("cmdInstall: %v\n%s", err, out)
	}
	if got := markedVersion(t); got != "1.0" {
		t.Errorf("marked version = %q, want 1.0 from the default", got)
	}
}

func TestInstallNoAtMarkPromptDoesNotMark(t *testing.T) {
	newInstallEnv(t, "none", serveAssets(false))

	if _, err := runInstall(t, proceedNoRun+"no\n"); err != nil {
		t.Fatalf("cmdInstall: %v", err)
	}
	if got := markedVersion(t); got != "" {
		t.Errorf("marked version = %q, want unmarked", got)
	}
}

func TestInstallAcceptsYesAtDefaultNoPrompts(t *testing.T) {
	newInstallEnv(t, "none", serveAssets(true))

	_, err := runInstall(t, "yes\nn\nYES\n")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 assets failed") {
		t.Errorf("err = %v, want 1 of 2 assets failed", err)
	}
	if got := markedVersion(t); got != "1.0" {
		t.Errorf("marked version = %q, want 1.0", got)
	}
}

func TestConfirm(t *testing.T) {
	for _, tc := range []struct {
		input string
		def   bool
		want  bool
	}{
		{"y\n", false, true},
		{"Yes\r\n", false, true},
		{" YES \n", false, true},
		{"n\n", true, false},
		{"no\n", true, false},
		{"NO\n", true, false},
		{"\n", true, true},
		{"\r\n", false, false},
		{"maybe\n", true, false},
		{"", true, false},  // end of input
		{"y", false, true}, // last line without a newline
	} {
		got := confirm(bufio.NewReader(strings.NewReader(tc.input)), "Q?", tc.def)
		if got != tc.want {
			t.Errorf("confirm(%q, def %v) = %v, want %v", tc.input, tc.def, got, tc.want)
		}
	}
}
