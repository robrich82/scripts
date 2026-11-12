package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestRunMainHelper is the child side of runMain: it runs main() with the
// arguments after "--" when the test binary is re-executed with
// MKV_TVSHOW_RUN_MAIN=1.
func TestRunMainHelper(t *testing.T) {
	if os.Getenv("MKV_TVSHOW_RUN_MAIN") != "1" {
		t.Skip("helper process only")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Args = append([]string{"mkv-tvshow"}, args...)
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	main()
	os.Exit(0)
}

// runMain runs main() in a child process with cwd dir, a config dir under cfgHome
// and no TMDB_API_KEY, and returns its combined output and exit error.
func runMain(t *testing.T, dir, cfgHome string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestRunMainHelper$", "--"}, args...)...)
	cmd.Dir = dir
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "TMDB_API_KEY="),
			strings.HasPrefix(kv, "PLEX_"),
			strings.HasPrefix(kv, "APPDATA="),
			strings.HasPrefix(kv, "XDG_CONFIG_HOME="),
			strings.HasPrefix(kv, "HOME="):
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "MKV_TVSHOW_RUN_MAIN=1", "APPDATA="+cfgHome, "XDG_CONFIG_HOME="+cfgHome, "HOME="+cfgHome)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestMain_DebandOnlyRunsWithoutTMDBKey(t *testing.T) {
	out, err := runMain(t, t.TempDir(), t.TempDir(), "--deband-only")
	if err != nil {
		t.Fatalf("--deband-only without TMDB_API_KEY failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "TMDB_API_KEY") {
		t.Errorf("--deband-only should not mention TMDB_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "Done. Updated: 0  Failed: 0") {
		t.Errorf("expected the deband summary, got:\n%s", out)
	}
}

func TestMain_NormalModeStillRequiresTMDBKey(t *testing.T) {
	out, err := runMain(t, t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatalf("normal mode without TMDB_API_KEY should fail, got:\n%s", out)
	}
	if !strings.Contains(out, "TMDB_API_KEY environment variable not set") {
		t.Errorf("expected the TMDB_API_KEY error, got:\n%s", out)
	}
}
