package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// searchTMDB finds one movie and one show, and knows both by ID.
type searchTMDB struct{}

func (searchTMDB) GetTVOriginByID(string) (OriginInfo, bool, error) {
	return OriginInfo{OrigLang: "ja"}, true, nil
}

func (searchTMDB) GetMovieOriginByID(string) (OriginInfo, bool, error) {
	return OriginInfo{OrigLang: "fr"}, true, nil
}

func (searchTMDB) SearchTV(string, string) ([]TVSearchCandidate, error) {
	return []TVSearchCandidate{{ID: "77", Name: "Show", Year: "2020"}}, nil
}

func (searchTMDB) SearchMovie(string, string) ([]MovieSearchCandidate, error) {
	return []MovieSearchCandidate{{ID: "42", Title: "Amelie", Year: "2001"}}, nil
}

// siblingLibrary makes a library root holding entry with the given files, and
// points APPDATA at a fresh directory.
func siblingLibrary(t *testing.T, entry string, files ...string) (root string, paths []string) {
	t.Helper()
	t.Setenv("APPDATA", t.TempDir())
	root = t.TempDir()
	dir := filepath.Join(root, entry)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		paths = append(paths, filepath.Join(dir, f))
		makeMKVFile(t, dir, f)
	}
	return root, paths
}

func writeSibling(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readSibling(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return m
}

// A confirmed movie ID goes into mkv-movie's tmdb_ids with a files entry for
// each of the movie's files, so mkv-movie --clean keeps it; mkv-movie's other
// keys are kept as they were.
func TestFetchOrigins_MovieIDSavedWithFiles(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "Amelie (2001).mkv", "Amelie (2001) - extras.mkv")
	cachePath := movieRatingCachePath()
	writeSibling(t, cachePath, `{"movies":{"9":"M"},"tmdb_ids":{"Coco:2017":"9"},"files":{"C:\\coco.mkv":"9"}}`)

	captureStdout(t, func() {
		origins := fetchOrigins(paths, root, "movie", searchTMDB{}, &fakePrompter{answer: "y"})
		if origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr for the confirmed movie", origins)
		}
	})

	saved := readSibling(t, cachePath)
	var movies bytes.Buffer
	if err := json.Compact(&movies, saved["movies"]); err != nil || movies.String() != `{"9":"M"}` {
		t.Errorf("movies = %s (%v); want it kept as it was", saved["movies"], err)
	}
	var ids, files map[string]string
	if err := json.Unmarshal(saved["tmdb_ids"], &ids); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(saved["files"], &files); err != nil {
		t.Fatal(err)
	}
	if ids["Amelie:2001"] != "42" || ids["Coco:2017"] != "9" {
		t.Errorf("tmdb_ids = %v; want Amelie:2001 → 42 added, Coco kept", ids)
	}
	if len(files) != 3 || files[paths[0]] != "42" || files[paths[1]] != "42" || files[`C:\coco.mkv`] != "9" {
		t.Errorf("files = %v; want both of the movie's files → 42 and Coco's kept", files)
	}
}

// A sibling cache that can't be parsed is reported and left byte for byte as
// it was, so the user can still restore it.
func TestFetchOrigins_CorruptSiblingCacheLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name, libType, entry, corrupt string
		cachePath                     func() string
	}{
		{"movie file", "movie", "Amelie (2001)", `{"tmdb_ids":`, movieRatingCachePath},
		{"movie tmdb_ids", "movie", "Amelie (2001)", `{"tmdb_ids":["not","a","map"]}`, movieRatingCachePath},
		{"movie files", "movie", "Amelie (2001)", `{"files":7}`, movieRatingCachePath},
		{"show cache", "tv", "Show (2020)", "\x00\x00\x00", tvShowCachePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, tc.entry, "a.mkv")
			writeSibling(t, tc.cachePath(), tc.corrupt)

			out := captureStdout(t, func() {
				fetchOrigins(paths, root, tc.libType, searchTMDB{}, &fakePrompter{answer: "y"})
			})

			if b, _ := os.ReadFile(tc.cachePath()); string(b) != tc.corrupt {
				t.Errorf("cache rewritten to %q; want it left as %q", b, tc.corrupt)
			}
			if !strings.Contains(out, "could not save to sibling cache") || !strings.Contains(out, "corrupt") {
				t.Errorf("save failure not reported as a corrupt file:\n%s", out)
			}
		})
	}
}

// A confirmed show ID is added to mkv-tvshow's show cache, keeping legacy
// numeric IDs as they were.
func TestFetchOrigins_ShowIDSaved(t *testing.T) {
	root, paths := siblingLibrary(t, "Show (2020)", "Show - S01E01.mkv")
	writeSibling(t, tvShowCachePath(), `{"Old Show": 123}`)

	captureStdout(t, func() {
		fetchOrigins(paths, root, "tv", searchTMDB{}, &fakePrompter{answer: "y"})
	})

	saved := readSibling(t, tvShowCachePath())
	if got := string(saved["Old Show"]); got != "123" {
		t.Errorf("Old Show = %s; want the numeric 123 kept", got)
	}
	if got := string(saved[filepath.Join(root, "Show (2020)")]); got != `"77"` {
		t.Errorf("new entry = %s; want \"77\" under the show's absolute path (saved: %v)", got, saved)
	}
}
