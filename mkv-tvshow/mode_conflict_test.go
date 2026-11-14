package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConflictingModes(t *testing.T) {
	modes := func(set ...string) []oneShotMode {
		var m []oneShotMode
		for _, name := range []string{"--clean", "--reset-rating", "--set-rating", "--deband-only", "--missing", "--language-only", "--title-check", "--ratings-only"} {
			on := false
			for _, s := range set {
				on = on || s == name
			}
			m = append(m, oneShotMode{name, on})
		}
		return m
	}
	for _, tc := range []struct {
		set  []string
		want []string
	}{
		{nil, nil},
		{[]string{"--missing"}, nil},
		{[]string{"--ratings-only"}, nil},
		{[]string{"--ratings-only", "--clean"}, []string{"--clean", "--ratings-only"}},
		{[]string{"--title-check", "--missing"}, []string{"--missing", "--title-check"}},
		{[]string{"--ratings-only", "--language-only", "--set-rating"}, []string{"--set-rating", "--language-only", "--ratings-only"}},
	} {
		if got := conflictingModes(modes(tc.set...)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("conflictingModes(%v) = %v; want %v", tc.set, got, tc.want)
		}
	}
}

func TestJoinNames(t *testing.T) {
	for in, want := range map[string]string{
		"a":     "a",
		"a,b":   "a and b",
		"a,b,c": "a, b and c",
	} {
		if got := joinNames(strings.Split(in, ",")); got != want {
			t.Errorf("joinNames(%q) = %q; want %q", in, got, want)
		}
	}
}

// #172: two modes stop the run before either does anything, where main used
// to run the first and silently drop the rest. No TMDB_API_KEY is set, so the
// check must also come before the key check.
func TestMain_ConflictingModes_Exit(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--clean", "--set-rating", "R16"}, "Error: --clean and --set-rating can't be combined; run them one at a time."},
		{[]string{"--deband-only", "--ratings-only"}, "Error: --deband-only and --ratings-only can't be combined; run them one at a time."},
		{[]string{"--title-check", "--ratings-only"}, "Error: --title-check and --ratings-only can't be combined; run them one at a time."},
		{[]string{"--language-only", "--missing", "--clean"}, "Error: --clean, --missing and --language-only can't be combined; run them one at a time."},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cfgHome := t.TempDir()
			cachePath := filepath.Join(cfgHome, "mkv-tvshow", "show_cache.json")
			if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
				t.Fatal(err)
			}
			// A show folder no longer on disk: --clean would prune it if it ran.
			cache, err := LoadCache(cachePath)
			if err != nil {
				t.Fatal(err)
			}
			cache.Set(filepath.Join(t.TempDir(), "Gone (2020)"), "1")
			if err := cache.Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatal(err)
			}

			out, err := runMain(t, t.TempDir(), cfgHome, tc.args...)

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("err = %v; want exit status 1:\n%s", err, out)
			}
			if got := strings.TrimSpace(out); got != tc.want {
				t.Errorf("output %q; want only %q", got, tc.want)
			}
			after, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("cache changed:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}
