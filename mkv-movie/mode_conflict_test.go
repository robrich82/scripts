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
		for _, name := range []string{"--clean", "--reset-rating", "--set-rating", "--deband-only", "--ratings-only", "--language-only"} {
			m = append(m, oneShotMode{name, false})
			for _, s := range set {
				if s == name {
					m[len(m)-1].set = true
				}
			}
		}
		return m
	}
	for _, tc := range []struct {
		set  []string
		want []string
	}{
		{nil, nil},
		{[]string{"--clean"}, nil},
		{[]string{"--language-only"}, nil},
		{[]string{"--ratings-only", "--clean"}, []string{"--clean", "--ratings-only"}},
		{[]string{"--deband-only", "--ratings-only"}, []string{"--deband-only", "--ratings-only"}},
		{[]string{"--set-rating", "--ratings-only", "--language-only"}, []string{"--set-rating", "--ratings-only", "--language-only"}},
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

// #177: two one-shot modes stop the run before either does anything, where
// main used to run the first and silently drop the second.
func TestMain_ConflictingModes_Exit(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--ratings-only", "--clean"}, "Error: --clean and --ratings-only can't be combined; run them one at a time."},
		{[]string{"--deband-only", "--ratings-only"}, "Error: --deband-only and --ratings-only can't be combined; run them one at a time."},
		{[]string{"--language-only", "--set-rating", "R16", "--clean"}, "Error: --clean, --set-rating and --language-only can't be combined; run them one at a time."},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cfgHome := t.TempDir()
			cachePath := filepath.Join(cfgHome, "mkv-movie", "movie_rating_cache.json")
			if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
				t.Fatal(err)
			}
			// A file no longer on disk: --clean would prune it if it ran.
			cache, _ := LoadMovieRatingCache(cachePath)
			gone := filepath.Join(t.TempDir(), "Gone (2020).mkv")
			cache.SetFileTMDBID(gone, "1")
			cache.Set("1", RatingPG)
			if err := cache.Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestRunMainHelper$", "--"}, tc.args...)...)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "MKV_MOVIE_RUN_MAIN=1", "TMDB_API_KEY=unused", "PLEX_URL=", "PLEX_TOKEN=",
				"APPDATA="+cfgHome, "XDG_CONFIG_HOME="+cfgHome, "HOME="+cfgHome)
			out, err := cmd.CombinedOutput()

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("err = %v; want exit status 1:\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
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
