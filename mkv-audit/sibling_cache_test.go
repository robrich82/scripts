package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// titleTMDB finds each movie by its title, and knows each ID's language.
type titleTMDB struct {
	ids   map[string]string // title → ID
	langs map[string]string // ID → original language
}

func (f titleTMDB) GetTVOriginByID(id string) (OriginInfo, bool, error) {
	return f.GetMovieOriginByID(id)
}

func (f titleTMDB) GetMovieOriginByID(id string) (OriginInfo, bool, error) {
	lang, ok := f.langs[id]
	return OriginInfo{OrigLang: lang}, ok, nil
}

func (f titleTMDB) SearchTV(title, year string) ([]TVSearchCandidate, error) {
	return []TVSearchCandidate{{ID: f.ids[title], Name: title, Year: year}}, nil
}

func (f titleTMDB) SearchMovie(title, year string) ([]MovieSearchCandidate, error) {
	return []MovieSearchCandidate{{ID: f.ids[title], Title: title, Year: year}}, nil
}

// noPrompter fails the test if anything is asked.
type noPrompter struct{ t *testing.T }

func (p noPrompter) Ask(q string) (string, error) {
	p.t.Errorf("unexpected prompt: %s", q)
	return "", nil
}

// siblingLibrary makes a library root holding entry with the given files, and
// points APPDATA at a fresh directory.
func siblingLibrary(t *testing.T, entry string, files ...string) (root string, paths []string) {
	t.Helper()
	t.Setenv("APPDATA", t.TempDir())
	root = t.TempDir()
	return root, addEntry(t, root, entry, files...)
}

// addEntry adds a content root holding files to the library at root.
func addEntry(t *testing.T, root, entry string, files ...string) (paths []string) {
	t.Helper()
	dir := filepath.Join(root, entry)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		paths = append(paths, filepath.Join(dir, f))
		makeMKVFile(t, dir, f)
	}
	return paths
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

// readMovieSections returns the tmdb_ids and files sections of mkv-movie's cache.
func readMovieSections(t *testing.T) (ids, files map[string]string) {
	t.Helper()
	saved := readSibling(t, movieRatingCachePath())
	if err := json.Unmarshal(saved["tmdb_ids"], &ids); err != nil {
		t.Fatalf("tmdb_ids: %v", err)
	}
	if err := json.Unmarshal(saved["files"], &files); err != nil {
		t.Fatalf("files: %v", err)
	}
	return ids, files
}

// assertNoTempFiles fails if a temp file from atomicWriteFile is left in dir.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// A confirmed movie ID goes into mkv-movie's tmdb_ids with a files entry for
// each of the movie's .mkv files, so mkv-movie --clean keeps it; mkv-movie's
// other keys are kept as they were.
func TestFetchOrigins_MovieIDSavedWithFiles(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "Amelie (2001).mkv", "Amelie (2001) - extras.mkv", "Amelie (2001).eng.mka")
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
	ids, files := readMovieSections(t)
	if ids["Amelie:2001"] != "42" || ids["Coco:2017"] != "9" {
		t.Errorf("tmdb_ids = %v; want Amelie:2001 → 42 added, Coco kept", ids)
	}
	if len(files) != 3 || files[paths[0]] != "42" || files[paths[1]] != "42" || files[`C:\coco.mkv`] != "9" {
		t.Errorf("files = %v; want both .mkv files → 42 (not the .mka) and Coco's kept", files)
	}
}

// Two movies in one run each get their own ID, against their own files.
func TestFetchOrigins_TwoMoviesGetTheirOwnIDs(t *testing.T) {
	root, amelie := siblingLibrary(t, "Amelie (2001)", "Amelie (2001).mkv")
	coco := addEntry(t, root, "Coco (2017)", "Coco (2017).mkv")
	tmdb := titleTMDB{
		ids:   map[string]string{"Amelie": "42", "Coco": "9"},
		langs: map[string]string{"42": "fr", "9": "es"},
	}

	captureStdout(t, func() {
		origins := fetchOrigins(append(amelie, coco...), root, "movie", tmdb, &fakePrompter{answer: "y"})
		if origins["Amelie (2001)"].OrigLang != "fr" || origins["Coco (2017)"].OrigLang != "es" {
			t.Errorf("origins = %v; want Amelie fr and Coco es", origins)
		}
	})

	ids, files := readMovieSections(t)
	if ids["Amelie:2001"] != "42" || ids["Coco:2017"] != "9" {
		t.Errorf("tmdb_ids = %v; want Amelie:2001 → 42, Coco:2017 → 9", ids)
	}
	if files[amelie[0]] != "42" || files[coco[0]] != "9" || len(files) != 2 {
		t.Errorf("files = %v; want each movie's file → its own ID", files)
	}
}

// On a first run neither sibling tool has a cache, or even a directory: the
// confirmed ID creates both.
func TestFetchOrigins_FirstRunCreatesSiblingCache(t *testing.T) {
	for _, tc := range []struct {
		libType, entry string
		cachePath      func() string
	}{
		{"movie", "Amelie (2001)", movieRatingCachePath},
		{"tv", "Show (2020)", tvShowCachePath},
	} {
		t.Run(tc.libType, func(t *testing.T) {
			root, paths := siblingLibrary(t, tc.entry, "a.mkv")
			out := captureStdout(t, func() {
				fetchOrigins(paths, root, tc.libType, searchTMDB{}, &fakePrompter{answer: "y"})
			})
			if strings.Contains(out, "[WARN]") {
				t.Errorf("unexpected warning:\n%s", out)
			}
			if saved := readSibling(t, tc.cachePath()); len(saved) == 0 {
				t.Error("nothing saved")
			}
		})
	}
}

// A cache holding null, or null sections, is empty rather than corrupt.
func TestFetchOrigins_NullMovieCacheIsEmpty(t *testing.T) {
	for _, content := range []string{`null`, `{"tmdb_ids":null,"files":null}`} {
		t.Run(content, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Amelie (2001)", "Amelie (2001).mkv")
			writeSibling(t, movieRatingCachePath(), content)

			out := captureStdout(t, func() {
				fetchOrigins(paths, root, "movie", searchTMDB{}, &fakePrompter{answer: "y"})
			})

			if strings.Contains(out, "[WARN]") {
				t.Errorf("unexpected warning:\n%s", out)
			}
			ids, files := readMovieSections(t)
			if ids["Amelie:2001"] != "42" || files[paths[0]] != "42" {
				t.Errorf("tmdb_ids = %v, files = %v; want Amelie → 42 in both", ids, files)
			}
		})
	}
}

// A file mkv-movie already maps keeps its ID: mkv-movie --clean would
// otherwise drop the old ID's rating and Plex state.
func TestSaveMovieTMDBIDEntry_KeepsExistingFileEntry(t *testing.T) {
	_, paths := siblingLibrary(t, "Amelie (2001)", "Amelie (2001).mkv", "Amelie (2001) - extras.mkv")
	b, _ := json.Marshal(map[string]map[string]string{"files": {paths[0]: "194"}})
	writeSibling(t, movieRatingCachePath(), string(b))

	if err := saveMovieTMDBIDEntry("Amelie", "2001", "42", paths); err != nil {
		t.Fatal(err)
	}

	_, files := readMovieSections(t)
	if files[paths[0]] != "194" || files[paths[1]] != "42" {
		t.Errorf("files = %v; want the mapped file kept at 194 and only the new one → 42", files)
	}
}

// A movie whose file mkv-movie already maps uses that ID, without searching
// or asking, even when mkv-movie keyed its title differently.
func TestFetchOrigins_MovieFoundByMappedFile(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie.(2001)", "Amelie.(2001).mkv")
	b, _ := json.Marshal(map[string]map[string]string{
		"tmdb_ids": {"Amelie (2001):": "194"},
		"files":    {paths[0]: "194"},
	})
	writeSibling(t, movieRatingCachePath(), string(b))
	tmdb := titleTMDB{langs: map[string]string{"194": "fr"}}

	captureStdout(t, func() {
		origins := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t})
		if origins["Amelie.(2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the mapped file's ID", origins)
		}
	})
}

// In a flat library the content root is the movie file itself.
func TestFetchOrigins_FlatMovieLibrary(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	root := t.TempDir()
	makeMKVFile(t, root, "Amelie (2001).mkv")
	path := filepath.Join(root, "Amelie (2001).mkv")

	captureStdout(t, func() {
		origins := fetchOrigins([]string{path}, root, "movie", searchTMDB{}, &fakePrompter{answer: "y"})
		if origins["Amelie (2001).mkv"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr for the flat movie", origins)
		}
	})

	ids, files := readMovieSections(t)
	if ids["Amelie:2001"] != "42" || files[path] != "42" || len(files) != 1 {
		t.Errorf("tmdb_ids = %v, files = %v; want Amelie:2001 and the file → 42", ids, files)
	}
}

// A sibling cache that can't be parsed is reported once, left byte for byte
// as it was so the user can still restore it, and the run still uses the IDs
// confirmed for it.
func TestFetchOrigins_CorruptSiblingCacheLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name, libType, corrupt string
		entries                [2]string
		cachePath              func() string
	}{
		{"movie file", "movie", `{"tmdb_ids":`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie tmdb_ids", "movie", `{"tmdb_ids":["not","a","map"]}`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie files", "movie", `{"files":7}`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie trailing data", "movie", `{"files":{}} {}`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie empty file", "movie", ``, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"show zero-filled", "tv", "\x00\x00\x00", [2]string{"Show (2020)", "Other (2021)"}, tvShowCachePath},
		{"show empty file", "tv", ``, [2]string{"Show (2020)", "Other (2021)"}, tvShowCachePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, tc.entries[0], "a.mkv")
			paths = append(paths, addEntry(t, root, tc.entries[1], "b.mkv")...)
			writeSibling(t, tc.cachePath(), tc.corrupt)

			out := captureStdout(t, func() {
				origins := fetchOrigins(paths, root, tc.libType, searchTMDB{}, &fakePrompter{answer: "y"})
				for _, e := range tc.entries {
					if origins[e].IsZero() {
						t.Errorf("origins[%q] unset (%v); want the confirmed ID used for this run", e, origins)
					}
				}
			})

			if b, _ := os.ReadFile(tc.cachePath()); string(b) != tc.corrupt {
				t.Errorf("cache rewritten to %q; want it left as %q", b, tc.corrupt)
			}
			assertNoTempFiles(t, filepath.Dir(tc.cachePath()))
			if n := strings.Count(out, "[WARN]"); n != 1 || !strings.Contains(out, "can't be loaded") || !strings.Contains(out, "corrupt") {
				t.Errorf("want one [WARN] naming a corrupt cache, got %d:\n%s", n, out)
			}
		})
	}
}

// mkv-tvshow ignores data after the first JSON value, so mkv-audit saves to
// such a cache too.
func TestFetchOrigins_ShowCacheTrailingDataAccepted(t *testing.T) {
	root, paths := siblingLibrary(t, "Show (2020)", "a.mkv")
	writeSibling(t, tvShowCachePath(), `{"Old Show":"5"} junk`)

	out := captureStdout(t, func() {
		fetchOrigins(paths, root, "tv", searchTMDB{}, &fakePrompter{answer: "y"})
	})

	if strings.Contains(out, "[WARN]") {
		t.Errorf("unexpected warning:\n%s", out)
	}
	saved := readSibling(t, tvShowCachePath())
	if string(saved["Old Show"]) != `"5"` || string(saved[filepath.Join(root, "Show (2020)")]) != `"77"` {
		t.Errorf("saved = %v; want Old Show kept and the new show → 77", saved)
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

// A save that fails is reported once, and the run stops saving to that cache
// but still uses the IDs. Windows can't replace a file another handle has
// open, which is how the save is made to fail.
func TestFetchOrigins_SaveFailureWarnsOnce(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs Windows file locking to fail the rename")
	}
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	paths = append(paths, addEntry(t, root, "Coco (2017)", "b.mkv")...)
	const content = `{"movies":{"9":"M"}}`
	writeSibling(t, movieRatingCachePath(), content)
	f, err := os.Open(movieRatingCachePath())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	out := captureStdout(t, func() {
		origins := fetchOrigins(paths, root, "movie", searchTMDB{}, &fakePrompter{answer: "y"})
		if len(origins) != 2 {
			t.Errorf("origins = %v; want both movies despite the failed save", origins)
		}
	})

	if n := strings.Count(out, "could not save"); n != 1 || !strings.Contains(out, "[WARN]") || !strings.Contains(out, "next run asks again") {
		t.Errorf("want one [WARN] save failure, got %d:\n%s", n, out)
	}
	f.Close()
	if b, _ := os.ReadFile(movieRatingCachePath()); string(b) != content {
		t.Errorf("cache = %q; want it left as %q", b, content)
	}
	assertNoTempFiles(t, filepath.Dir(movieRatingCachePath()))
}

// A rename that fails leaves the target as it was and no temp file behind.
func TestAtomicWriteFile_FailedRenameCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cache.json")
	// A non-empty directory can't be replaced by a file on any platform.
	if err := os.MkdirAll(filepath.Join(target, "keep"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := atomicWriteFile(target, []byte(`{}`)); err == nil {
		t.Fatal("atomicWriteFile succeeded; want the rename to fail")
	}

	if fi, err := os.Stat(filepath.Join(target, "keep")); err != nil || !fi.IsDir() {
		t.Errorf("target changed: %v", err)
	}
	assertNoTempFiles(t, dir)
}
