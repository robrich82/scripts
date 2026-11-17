package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

// titleTMDB finds each title by name, and knows each ID's language. An ID
// with an empty language fails to load. A title it doesn't know has no
// search results.
type titleTMDB struct {
	ids      map[string]string // title → ID
	langs    map[string]string // ID → original language
	onSearch func()            // runs on each search, when set
	onOrigin func(id string)   // runs on each ID lookup, when set
}

func (f titleTMDB) GetTVOriginByID(id string) (OriginInfo, bool, error) {
	return f.GetMovieOriginByID(id)
}

func (f titleTMDB) GetMovieOriginByID(id string) (OriginInfo, bool, error) {
	if f.onOrigin != nil {
		f.onOrigin(id)
	}
	lang, ok := f.langs[id]
	if ok && lang == "" {
		return OriginInfo{}, false, errors.New("TMDB is down")
	}
	return OriginInfo{OrigLang: lang}, ok, nil
}

func (f titleTMDB) SearchTV(title, year string) ([]TVSearchCandidate, error) {
	if f.onSearch != nil {
		f.onSearch()
	}
	if f.ids[title] == "" {
		return nil, nil
	}
	return []TVSearchCandidate{{ID: f.ids[title], Name: title, Year: year}}, nil
}

func (f titleTMDB) SearchMovie(title, year string) ([]MovieSearchCandidate, error) {
	if f.onSearch != nil {
		f.onSearch()
	}
	if f.ids[title] == "" {
		return nil, nil
	}
	return []MovieSearchCandidate{{ID: f.ids[title], Title: title, Year: year}}, nil
}

// scriptPrompter gives its answers in turn, then fails as stdin at EOF does.
type scriptPrompter struct {
	answers []string
	asked   []string
}

func (p *scriptPrompter) Ask(q string) (string, error) {
	p.asked = append(p.asked, q)
	if len(p.answers) == 0 {
		return "", errors.New("EOF")
	}
	a := p.answers[0]
	p.answers = p.answers[1:]
	return a, nil
}

// noPrompter fails the test if anything is asked.
type noPrompter struct{ t *testing.T }

func (p noPrompter) Ask(q string) (string, error) {
	p.t.Errorf("unexpected prompt: %s", q)
	return "", nil
}

// setConfigDir points os.UserConfigDir into dir: APPDATA on Windows,
// XDG_CONFIG_HOME on Linux, HOME on macOS.
func setConfigDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

// tvShowCachePath and movieRatingCachePath return where the sibling caches
// are, under the config dir the test set.
func tvShowCachePath() string { return mustCachePath(tvShowSibling) }

func movieRatingCachePath() string { return mustCachePath(movieSibling) }

func mustCachePath(c siblingCache) string {
	p, err := c.path()
	if err != nil {
		panic(err)
	}
	return p
}

// siblingLibrary makes a library root holding entry with the given files, and
// points the config dir at a fresh directory.
func siblingLibrary(t *testing.T, entry string, files ...string) (root string, paths []string) {
	t.Helper()
	setConfigDir(t, t.TempDir())
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
// each of the movie's .mkv files, extras in subfolders included, so
// mkv-movie --clean keeps it; mkv-movie's other keys are kept as they were.
func TestFetchOrigins_MovieIDSavedWithFiles(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "Amelie (2001).mkv")
	paths = append(paths, addEntry(t, root, filepath.Join("Amelie (2001)", "Extras"), "Trailer.mkv")...)
	paths = append(paths, addEntry(t, root, "Amelie (2001)", "Amelie (2001).eng.mka")...)
	cachePath := movieRatingCachePath()
	writeSibling(t, cachePath, `{"movies":{"9":"M"},"tmdb_ids":{"Coco:2017":"9"},"files":{"C:\\coco.mkv":"9"}}`)

	captureStdout(t, func() {
		origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, &fakePrompter{answer: "y"})
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
		origins, _ := fetchOrigins(append(amelie, coco...), root, "movie", tmdb, &fakePrompter{answer: "y"})
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
func TestSaveMovieTMDBIDEntries_KeepsExistingFileEntry(t *testing.T) {
	_, paths := siblingLibrary(t, "Amelie (2001)", "Amelie (2001).mkv", "Amelie (2001) - extras.mkv")
	b, _ := json.Marshal(map[string]map[string]string{"files": {paths[0]: "194"}})
	writeSibling(t, movieRatingCachePath(), string(b))

	if err := saveMovieTMDBIDEntries(movieRatingCachePath(), []movieEntry{{title: "Amelie", year: "2001", id: "42", files: paths}}); err != nil {
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
		origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t})
		if origins["Amelie.(2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the mapped file's ID", origins)
		}
	})
}

// In a flat library the content root is the movie file itself.
func TestFetchOrigins_FlatMovieLibrary(t *testing.T) {
	setConfigDir(t, t.TempDir())
	root := t.TempDir()
	makeMKVFile(t, root, "Amelie (2001).mkv")
	path := filepath.Join(root, "Amelie (2001).mkv")

	captureStdout(t, func() {
		origins, _ := fetchOrigins([]string{path}, root, "movie", searchTMDB{}, &fakePrompter{answer: "y"})
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
		// Every section mkv-movie decodes as a string map, so mkv-audit calls
		// the cache bad exactly when mkv-movie refuses to load it.
		{"movie movies", "movie", `{"movies":{"9":1}}`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie applied", "movie", `{"applied":[]}`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie plex_ratings", "movie", `{"plex_ratings":"x"}`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie streams_applied", "movie", `{"streams_applied":{"1":true}}`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie meta_applied", "movie", `{"meta_applied":7}`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"movie not an object", "movie", `[]`, [2]string{"Amelie (2001)", "Coco (2017)"}, movieRatingCachePath},
		{"show zero-filled", "tv", "\x00\x00\x00", [2]string{"Show (2020)", "Other (2021)"}, tvShowCachePath},
		{"show empty file", "tv", ``, [2]string{"Show (2020)", "Other (2021)"}, tvShowCachePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, tc.entries[0], "a.mkv")
			paths = append(paths, addEntry(t, root, tc.entries[1], "b.mkv")...)
			writeSibling(t, tc.cachePath(), tc.corrupt)

			out := captureStdout(t, func() {
				origins, _ := fetchOrigins(paths, root, tc.libType, searchTMDB{}, &fakePrompter{answer: "y"})
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

// A failed save is reported once and the run still uses the IDs. The save is
// retried with each later confirmation and once more at the end, and what is
// finally lost is named. Windows can't replace a file another handle has open,
// which is how the save is made to fail.
func TestFetchOrigins_SaveFailureRetried(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs Windows file locking to fail the rename")
	}
	for _, tc := range []struct {
		name          string
		releaseSearch int  // close the handle on this search (1-based); 0 = never mid-run
		releaseFinal  bool // close the handle before the final retry
		wantSaved     bool
	}{
		{"held throughout", 0, false, false},
		{"released before the final retry", 0, true, true},
		{"released mid-run", 2, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
			paths = append(paths, addEntry(t, root, "Coco (2017)", "b.mkv")...)
			const content = `{"movies":{"9":"M"}}`
			writeSibling(t, movieRatingCachePath(), content)
			f, err := os.Open(movieRatingCachePath())
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			searches := 0
			tmdb := titleTMDB{
				ids:   map[string]string{"Amelie": "42", "Coco": "9"},
				langs: map[string]string{"42": "fr", "9": "es"},
				onSearch: func() {
					if searches++; searches == tc.releaseSearch {
						f.Close()
					}
				},
			}
			old := beforeFinalSiblingSave
			beforeFinalSiblingSave = func() {
				if tc.releaseFinal {
					f.Close()
				}
			}
			t.Cleanup(func() { beforeFinalSiblingSave = old })

			out := captureStdout(t, func() {
				if origins, _ := fetchOrigins(paths, root, "movie", tmdb, &fakePrompter{answer: "y"}); len(origins) != 2 {
					t.Errorf("origins = %v; want both movies despite the failed save", origins)
				}
			})

			if n := strings.Count(out, "could not save"); n != 1 || !strings.Contains(out, "Close any program") {
				t.Errorf("want one save failure with the close-the-program hint, got %d:\n%s", n, out)
			}
			f.Close()
			if !tc.wantSaved {
				if !strings.Contains(out, "Amelie (2001) (id 42), Coco (2017) (id 9)") || !strings.Contains(out, "next run asks for them again") {
					t.Errorf("want both IDs named as lost:\n%s", out)
				}
				if b, _ := os.ReadFile(movieRatingCachePath()); string(b) != content {
					t.Errorf("cache = %q; want it left as %q", b, content)
				}
			} else {
				if strings.Contains(out, "lost") || !strings.Contains(out, "mkv-movie's cache saved") {
					t.Errorf("want the save to recover:\n%s", out)
				}
				if ids, _ := readMovieSections(t); ids["Amelie:2001"] != "42" || ids["Coco:2017"] != "9" {
					t.Errorf("tmdb_ids = %v; want both movies saved", ids)
				}
			}
			assertNoTempFiles(t, filepath.Dir(movieRatingCachePath()))
		})
	}
}

// The retry, on any platform and for both caches: a cache that turns corrupt
// after loading fails every save until it is put right. The run still uses
// the IDs; the save is retried with each later confirmation and once more at
// the end, and what is finally lost is named.
func TestFetchOrigins_SaveFailureRetriedAnyPlatform(t *testing.T) {
	type library struct {
		libType   string
		entries   [2]string
		ids       map[string]string // title → ID
		cachePath func() string
		saved     func(t *testing.T, root string) bool // both IDs are in the cache
	}
	movies := library{"movie", [2]string{"Amelie (2001)", "Coco (2017)"}, map[string]string{"Amelie": "42", "Coco": "9"}, movieRatingCachePath,
		func(t *testing.T, _ string) bool {
			ids, _ := readMovieSections(t)
			return ids["Amelie:2001"] == "42" && ids["Coco:2017"] == "9"
		}}
	shows := library{"tv", [2]string{"Show (2020)", "Other (2021)"}, map[string]string{"Show": "77", "Other": "78"}, tvShowCachePath,
		func(t *testing.T, root string) bool {
			saved := readSibling(t, tvShowCachePath())
			return string(saved[filepath.Join(root, "Show (2020)")]) == `"77"` && string(saved[filepath.Join(root, "Other (2021)")]) == `"78"`
		}}
	for _, lib := range []library{movies, shows} {
		for _, tc := range []struct {
			name         string
			repairSearch int  // put the cache right on this search (1-based); 0 = never mid-run
			repairFinal  bool // put the cache right before the final retry
			wantSaved    bool
		}{
			{"broken throughout", 0, false, false},
			{"repaired before the final retry", 0, true, true},
			{"repaired mid-run", 2, false, true},
		} {
			t.Run(lib.libType+"/"+tc.name, func(t *testing.T) {
				root, paths := siblingLibrary(t, lib.entries[0], "a.mkv")
				paths = append(paths, addEntry(t, root, lib.entries[1], "b.mkv")...)
				const good, corrupt = `{"kept":"1"}`, `{"kept":`
				writeSibling(t, lib.cachePath(), good)
				searches := 0
				tmdb := titleTMDB{
					ids:   lib.ids,
					langs: map[string]string{"42": "fr", "9": "es", "77": "ja", "78": "ko"},
					onSearch: func() {
						switch searches++; searches {
						case 1: // after the load, before the first save
							writeSibling(t, lib.cachePath(), corrupt)
						case tc.repairSearch:
							writeSibling(t, lib.cachePath(), good)
						}
					},
				}
				old := beforeFinalSiblingSave
				beforeFinalSiblingSave = func() {
					if tc.repairFinal {
						writeSibling(t, lib.cachePath(), good)
					}
				}
				t.Cleanup(func() { beforeFinalSiblingSave = old })

				out := captureStdout(t, func() {
					if origins, _ := fetchOrigins(paths, root, lib.libType, tmdb, &fakePrompter{answer: "y"}); len(origins) != 2 {
						t.Errorf("origins = %v; want both despite the failed save", origins)
					}
				})

				if n := strings.Count(out, "could not save"); n != 1 || !strings.Contains(out, "changed since mkv-audit loaded it") {
					t.Errorf("want one save failure saying the cache changed, got %d:\n%s", n, out)
				}
				if !tc.wantSaved {
					lost := lib.entries[0] + " (id " + lib.ids[strings.Fields(lib.entries[0])[0]] + "), " +
						lib.entries[1] + " (id " + lib.ids[strings.Fields(lib.entries[1])[0]] + ")"
					if !strings.Contains(out, lost) || !strings.Contains(out, "next run asks for them again") {
						t.Errorf("want %s named as lost:\n%s", lost, out)
					}
					if b, _ := os.ReadFile(lib.cachePath()); string(b) != corrupt {
						t.Errorf("cache = %q; want it left as %q", b, corrupt)
					}
				} else {
					if strings.Contains(out, "lost") || !strings.Contains(out, "cache saved") {
						t.Errorf("want the save to recover:\n%s", out)
					}
					if !lib.saved(t, root) || string(readSibling(t, lib.cachePath())["kept"]) != `"1"` {
						t.Errorf("cache = %s; want both IDs saved and the other key kept", readSibling(t, lib.cachePath()))
					}
				}
				assertNoTempFiles(t, filepath.Dir(lib.cachePath()))
			})
		}
	}
}

// Windows paths are case-insensitive: a show mkv-tvshow keyed under another
// spelling of its path is used without asking, and saving one updates the
// key as spelled rather than adding a second.
func TestFetchOrigins_ShowPathAnySpelling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("paths are case-sensitive here")
	}
	t.Run("lookup", func(t *testing.T) {
		root, paths := siblingLibrary(t, "Show (2020)", "a.mkv")
		writeSibling(t, tvShowCachePath(), mustJSON(t, map[string]string{strings.ToUpper(filepath.Join(root, "Show (2020)")): "77"}))
		tmdb := titleTMDB{langs: map[string]string{"77": "ja"}}

		captureStdout(t, func() {
			if origins, _ := fetchOrigins(paths, root, "tv", tmdb, noPrompter{t}); origins["Show (2020)"].OrigLang != "ja" {
				t.Errorf("origins = %v; want ja from the cached ID", origins)
			}
		})
	})
	t.Run("save", func(t *testing.T) {
		root, paths := siblingLibrary(t, "Show (2020)", "a.mkv")
		upper := strings.ToUpper(filepath.Join(root, "Show (2020)"))
		// A value mkv-tvshow skips, so the show is asked about.
		writeSibling(t, tvShowCachePath(), mustJSON(t, map[string]any{upper: true}))

		captureStdout(t, func() {
			fetchOrigins(paths, root, "tv", searchTMDB{}, &fakePrompter{answer: "y"})
		})

		saved := readSibling(t, tvShowCachePath())
		if len(saved) != 1 || string(saved[upper]) != `"77"` {
			t.Errorf("saved = %v; want only %s → \"77\"", saved, upper)
		}
	})
}

// A write replaces the file's contents, leaves no temp file, and (where
// modes mean anything) leaves the file readable by the sibling tools.
func TestAtomicWriteFile_ReplacesFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cache.json")
	writeSibling(t, target, `{"old":true}`)

	if err := atomicWriteFile(target, []byte(`{"new":true}`)); err != nil {
		t.Fatal(err)
	}

	if b, err := os.ReadFile(target); err != nil || string(b) != `{"new":true}` {
		t.Errorf("contents = %q (%v); want the new JSON", b, err)
	}
	if fi, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0644 {
		t.Errorf("mode = %v; want 0644", fi.Mode().Perm())
	}
	assertNoTempFiles(t, dir)
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

// mustJSON marshals v for a test cache.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A show mkv-tvshow already knows is used without searching or asking,
// whether keyed by its absolute path, by its bare folder name (legacy), or
// with a numeric ID (the Python version's format).
func TestFetchOrigins_ShowCacheHit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cache func(root string) string
	}{
		{"absolute key", func(root string) string {
			return mustJSON(t, map[string]string{filepath.Join(root, "Show (2020)"): "77"})
		}},
		{"bare folder name", func(string) string { return `{"Show (2020)":"77"}` }},
		{"numeric id", func(string) string { return `{"Show (2020)":77}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Show (2020)", "a.mkv")
			writeSibling(t, tvShowCachePath(), tc.cache(root))
			tmdb := titleTMDB{langs: map[string]string{"77": "ja"}}

			captureStdout(t, func() {
				origins, _ := fetchOrigins(paths, root, "tv", tmdb, noPrompter{t})
				if origins["Show (2020)"].OrigLang != "ja" {
					t.Errorf("origins = %v; want ja from cached ID 77", origins)
				}
			})
		})
	}
}

// An ID whose origin TMDB can't find, or fails to fetch, is not saved.
func TestFetchOrigins_UnloadableIDNotSaved(t *testing.T) {
	for _, tc := range []struct {
		name, libType, entry string
		langs                map[string]string
		cachePath            func() string
	}{
		{"movie not found", "movie", "Amelie (2001)", map[string]string{}, movieRatingCachePath},
		{"movie fetch error", "movie", "Amelie (2001)", map[string]string{"42": ""}, movieRatingCachePath},
		{"show not found", "tv", "Amelie (2001)", map[string]string{}, tvShowCachePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, tc.entry, "a.mkv")
			tmdb := titleTMDB{ids: map[string]string{"Amelie": "42"}, langs: tc.langs}

			captureStdout(t, func() {
				fetchOrigins(paths, root, tc.libType, tmdb, &fakePrompter{answer: "y"})
			})

			if b, err := os.ReadFile(tc.cachePath()); !os.IsNotExist(err) {
				t.Errorf("cache saved (%v): %s", err, b)
			}
		})
	}
}

// A typed answer that isn't all digits is asked again, never saved as an ID.
func TestFetchOrigins_ManualIDMustBeDigits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		libType string // "" → movie
		ids     map[string]string
		answers []string
		wantID  string
	}{
		{"accept prompt", "", map[string]string{"Amelie": "42"}, []string{"tt0133093", "yes", "603"}, "603"},
		{"no results prompt", "", nil, []string{"skip", "603"}, "603"},
		{"no results then blank", "", nil, []string{"skip", ""}, ""},
		// The tools key their caches by the ID's text, as TMDB writes it.
		{"leading zeros dropped", "", nil, []string{"0603"}, "603"},
		{"all zeros", "", map[string]string{"Amelie": "42"}, []string{"000", "603"}, "603"},
		{"show", "tv", map[string]string{"Amelie": "42"}, []string{"-1", "0603"}, "603"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
			tmdb := titleTMDB{ids: tc.ids, langs: map[string]string{"42": "fr", "603": "en"}}
			p := &scriptPrompter{answers: tc.answers}
			libType, cachePath := "movie", movieRatingCachePath()
			if tc.libType == "tv" {
				libType, cachePath = "tv", tvShowCachePath()
			}

			captureStdout(t, func() {
				fetchOrigins(paths, root, libType, tmdb, p)
			})

			if len(p.asked) != len(tc.answers) {
				t.Errorf("asked %d times; want %d:\n%s", len(p.asked), len(tc.answers), strings.Join(p.asked, "\n"))
			}
			if tc.wantID == "" {
				if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
					t.Errorf("cache saved; want nothing saved (%v)", err)
				}
				return
			}
			if libType == "tv" {
				saved := readSibling(t, cachePath)
				if key := filepath.Join(root, "Amelie (2001)"); len(saved) != 1 || string(saved[key]) != `"`+tc.wantID+`"` {
					t.Errorf("show cache = %v; want only %s → %s", saved, key, tc.wantID)
				}
				return
			}
			ids, files := readMovieSections(t)
			if len(ids) != 1 || ids["Amelie:2001"] != tc.wantID || len(files) != 1 || files[paths[0]] != tc.wantID {
				t.Errorf("tmdb_ids = %v, files = %v; want only %s", ids, files, tc.wantID)
			}
		})
	}
}

func TestNormalizeTMDBID(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"603", "603", true},
		{"00603", "603", true},
		{strings.Repeat("9", 40), strings.Repeat("9", 40), true}, // TMDB answers "not found"
		{"0", "", false},
		{"000", "", false},
		{"", "", false},
		{" 603", "", false}, // the prompt trims; the ID itself has no spaces
		{"+5", "", false},
		{"-1", "", false},
		{"６０３", "", false}, // full-width digits
		{"tt0133093", "", false},
	} {
		if got, ok := normalizeTMDBID(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("normalizeTMDBID(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// When stdin ends, the rest of the run skips TMDB lookups instead of taking
// the empty answer as "accept".
func TestFetchOrigins_NoInputSkipsLookups(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	paths = append(paths, addEntry(t, root, "Coco (2017)", "b.mkv")...)
	p := &scriptPrompter{}

	out := captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, p); len(origins) != 0 {
			t.Errorf("origins = %v; want none", origins)
		}
	})

	if len(p.asked) != 1 || strings.Count(out, "no input") != 1 {
		t.Errorf("asked %d times; want 1, and one 'no input' line:\n%s", len(p.asked), out)
	}
	if _, err := os.Stat(movieRatingCachePath()); !os.IsNotExist(err) {
		t.Errorf("cache saved; want nothing saved (%v)", err)
	}
}

// A TV run doesn't load or warn about mkv-movie's cache, and vice versa.
func TestFetchOrigins_OnlyOwnLibraryCacheLoaded(t *testing.T) {
	for _, tc := range []struct {
		libType, entry string
		otherCache     func() string
	}{
		{"tv", "Show (2020)", movieRatingCachePath},
		{"movie", "Amelie (2001)", tvShowCachePath},
	} {
		t.Run(tc.libType, func(t *testing.T) {
			root, paths := siblingLibrary(t, tc.entry, "a.mkv")
			writeSibling(t, tc.otherCache(), `{"tmdb_ids":`)

			out := captureStdout(t, func() {
				fetchOrigins(paths, root, tc.libType, searchTMDB{}, &fakePrompter{answer: "y"})
			})

			if strings.Contains(out, "[WARN]") {
				t.Errorf("warned about the other library type's cache:\n%s", out)
			}
		})
	}
}

// Without a config dir (no APPDATA on Windows, no XDG_CONFIG_HOME or HOME
// elsewhere) the sibling caches can't be found: one line says so, with the
// reason, and no save is tried (or reported) per directory.
func TestFetchOrigins_NoConfigDirWarnsOnce(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	paths = append(paths, addEntry(t, root, "Coco (2017)", "b.mkv")...)
	setConfigDir(t, "")
	_, wantErr := os.UserConfigDir()
	if wantErr == nil {
		t.Skip("os.UserConfigDir still finds a config dir here")
	}

	out := captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, &fakePrompter{answer: "y"}); len(origins) != 2 {
			t.Errorf("origins = %v; want both movies", origins)
		}
	})

	if strings.Count(out, "[WARN]") != 1 || !strings.Contains(out, "can't be found ("+wantErr.Error()+")") {
		t.Errorf("want one warning giving os.UserConfigDir's reason (%v):\n%s", wantErr, out)
	}
	if strings.Contains(out, "could not save") || strings.Contains(strings.ToLower(out), "close any program") {
		t.Errorf("want no save failures:\n%s", out)
	}
}

// A cache that turns corrupt after loading fails to save with a message that
// says so, not one telling the user to close other programs, and is left as
// it is.
func TestFetchOrigins_CacheCorruptedMidRunSaveHint(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	const corrupt = `{"tmdb_ids":`
	tmdb := titleTMDB{
		ids:      map[string]string{"Amelie": "42"},
		langs:    map[string]string{"42": "fr"},
		onSearch: func() { writeSibling(t, movieRatingCachePath(), corrupt) },
	}

	out := captureStdout(t, func() {
		fetchOrigins(paths, root, "movie", tmdb, &fakePrompter{answer: "y"})
	})

	if !strings.Contains(out, "could not save") || strings.Contains(strings.ToLower(out), "close any program") {
		t.Errorf("want a save failure without the close-the-program hint:\n%s", out)
	}
	if b, _ := os.ReadFile(movieRatingCachePath()); string(b) != corrupt {
		t.Errorf("cache = %q; want it left as %q", b, corrupt)
	}
}

// The load warning says every title will be looked up and asked about, and
// for a corrupt show cache, what deleting it costs.
func TestFetchOrigins_LoadWarningSaysWhatHappens(t *testing.T) {
	for _, tc := range []struct {
		libType, entry string
		cachePath      func() string
		want           []string
	}{
		{"movie", "Amelie (2001)", movieRatingCachePath, []string{"every movie", "asked"}},
		{"tv", "Show (2020)", tvShowCachePath, []string{"every show", "asked", "--clean", "rating_cache.json", "missing_ignore.json"}},
	} {
		t.Run(tc.libType, func(t *testing.T) {
			root, paths := siblingLibrary(t, tc.entry, "a.mkv")
			writeSibling(t, tc.cachePath(), `{`)

			out := captureStdout(t, func() {
				fetchOrigins(paths, root, tc.libType, searchTMDB{}, &fakePrompter{answer: "y"})
			})

			for _, w := range tc.want {
				if !strings.Contains(strings.ToLower(out), strings.ToLower(w)) {
					t.Errorf("warning lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

// A cache that can't be read (here, a directory in its place) is not called
// corrupt: the hint is to close whatever holds it, and it is left alone.
func TestFetchOrigins_UnreadableCacheNotCalledCorrupt(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	keep := filepath.Join(movieRatingCachePath(), "keep")
	if err := os.MkdirAll(keep, 0755); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, &fakePrompter{answer: "y"}); len(origins) != 1 {
			t.Errorf("origins = %v; want the confirmed movie", origins)
		}
	})

	if !strings.Contains(out, "can't be loaded") || strings.Contains(out, "corrupt") || !strings.Contains(out, "Close any program") {
		t.Errorf("want a load warning with the close-the-program hint, not 'corrupt':\n%s", out)
	}
	if fi, err := os.Stat(keep); err != nil || !fi.IsDir() {
		t.Errorf("cache path changed: %v", err)
	}
	assertNoTempFiles(t, filepath.Dir(movieRatingCachePath()))
	assertNoTempFiles(t, movieRatingCachePath())
}

// Two content roots with the same title and year are asked about once.
func TestFetchOrigins_SameMovieKeyAskedOnce(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	makeMKVFile(t, root, "Amelie (2001).mkv")
	paths = append(paths, filepath.Join(root, "Amelie (2001).mkv"))
	p := &scriptPrompter{answers: []string{"y", "y"}}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, p); len(origins) != 2 {
			t.Errorf("origins = %v; want both roots", origins)
		}
	})

	if len(p.asked) != 1 {
		t.Errorf("asked %d times; want once:\n%s", len(p.asked), strings.Join(p.asked, "\n"))
	}
}

// A root whose files mkv-movie maps to different movies (a collection) takes
// the ID most of them map to, the lowest on a tie, counting files under any
// spelling and in legacy meta_applied. Nothing is asked or saved, so
// mkv-movie --clean has nothing of mkv-audit's to prune.
func TestFetchOrigins_CollectionUsesMostMappedID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cache  func(paths []string) map[string]map[string]string
		wantID string
	}{
		{"most files", func(p []string) map[string]map[string]string {
			return map[string]map[string]string{"files": {p[0]: "24", p[1]: "393"}, "meta_applied": {p[2]: "393"}}
		}, "393"},
		{"tie goes to the lowest", func(p []string) map[string]map[string]string {
			return map[string]map[string]string{"files": {p[0]: "393", p[1]: "24"}}
		}, "24"},
		{"lowest as a number", func(p []string) map[string]map[string]string {
			return map[string]map[string]string{"files": {p[0]: "1000", p[1]: "999"}}
		}, "999"},
		// mkv-movie keeps a typed ID as typed; one that isn't an ID is skipped.
		{"typed with a leading zero", func(p []string) map[string]map[string]string {
			return map[string]map[string]string{"files": {p[0]: "0393", p[1]: "393", p[2]: "24"}}
		}, "393"},
		{"not an ID", func(p []string) map[string]map[string]string {
			return map[string]map[string]string{"files": {p[0]: "abc", p[1]: "abc", p[2]: "24"}}
		}, "24"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Kill Bill Collection", "Vol 1.mkv", "Vol 2.mkv", "Extra.mkv")
			mapped := slices.Clone(paths)
			if runtime.GOOS == "windows" {
				mapped[0] = strings.ToUpper(mapped[0])
			}
			writeSibling(t, movieRatingCachePath(), mustJSON(t, tc.cache(mapped)))
			before, _ := os.ReadFile(movieRatingCachePath())
			tmdb := titleTMDB{ids: map[string]string{"Kill Bill Collection": "42"},
				langs: map[string]string{"24": "en", "42": "fr", "393": "ja", "999": "de", "1000": "it"}}

			captureStdout(t, func() {
				origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t})
				if got, want := origins["Kill Bill Collection"].OrigLang, tmdb.langs[tc.wantID]; got != want {
					t.Errorf("origin = %q; want %q from ID %s", got, want, tc.wantID)
				}
			})

			if after, _ := os.ReadFile(movieRatingCachePath()); string(after) != string(before) {
				t.Errorf("cache rewritten:\n%s\nwant it left as\n%s", after, before)
			}
		})
	}
}

// A movie found by a title key alone (as older mkv-audit saved it) gets its
// .mkv file mapped to the ID, so mkv-movie --clean keeps it; the next run
// finds the mapping and leaves the cache alone.
func TestFetchOrigins_TitleKeyHitMapsFiles(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv", "a.eng.mka")
	writeSibling(t, movieRatingCachePath(), `{"tmdb_ids":{"Amelie:2001":"194"}}`)
	tmdb := titleTMDB{langs: map[string]string{"194": "fr"}}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the title key", origins)
		}
	})

	ids, files := readMovieSections(t)
	if len(ids) != 1 || ids["Amelie:2001"] != "194" || len(files) != 1 || files[paths[0]] != "194" {
		t.Errorf("tmdb_ids = %v, files = %v; want the title key kept and the .mkv → 194", ids, files)
	}
	before, _ := os.ReadFile(movieRatingCachePath())
	captureStdout(t, func() { fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}) })
	if after, _ := os.ReadFile(movieRatingCachePath()); string(after) != string(before) {
		t.Errorf("second run rewrote the cache:\n%s", after)
	}
}

// A cache mkv-movie would refuse to load (here a rating that isn't a string)
// isn't used at all, though its other sections parse: the movie is searched
// and asked about, as the warning says, and the cache is left alone.
func TestFetchOrigins_UnloadableMovieCacheNotUsed(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	bad := mustJSON(t, map[string]any{"movies": map[string]int{"194": 5}, "files": map[string]string{paths[0]: "194"}})
	writeSibling(t, movieRatingCachePath(), bad)
	tmdb := titleTMDB{ids: map[string]string{"Amelie": "42"}, langs: map[string]string{"42": "fr", "194": "ja"}}
	p := &scriptPrompter{answers: []string{"y"}}

	out := captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", tmdb, p); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the confirmed search result", origins)
		}
	})

	if len(p.asked) != 1 || !strings.Contains(out, "Every movie is searched") {
		t.Errorf("asked %d times; want once, after the load warning:\n%s", len(p.asked), out)
	}
	if after, _ := os.ReadFile(movieRatingCachePath()); string(after) != bad {
		t.Errorf("cache = %s; want it left as %s", after, bad)
	}
}

// mkv-movie's per-file mapping is used without asking: under any spelling of
// the path (Windows), from legacy meta_applied, ahead of a title:YEAR key
// that disagrees, and for files under the root this run didn't scan. A
// sibling folder sharing the root's name as a prefix doesn't count.
func TestFetchOrigins_MovieFileMappingUsed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flat  bool
		cache func(root, path string) map[string]map[string]string
	}{
		{"meta_applied", false, func(_, path string) map[string]map[string]string {
			return map[string]map[string]string{"meta_applied": {path: "194"}}
		}},
		{"beats title key", false, func(_, path string) map[string]map[string]string {
			return map[string]map[string]string{"tmdb_ids": {"Amelie:2001": "1"}, "files": {path: "194"}}
		}},
		{"unscanned file under root", false, func(root, _ string) map[string]map[string]string {
			return map[string]map[string]string{"files": {
				filepath.Join(root, "Amelie (2001)", "old", "Amelie.mkv"): "194",
				filepath.Join(root, "Amelie (2001) extras", "x.mkv"):      "5",
			}}
		}},
		{"flat file", true, func(_, path string) map[string]map[string]string {
			return map[string]map[string]string{"files": {path: "194"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setConfigDir(t, t.TempDir())
			root := t.TempDir()
			var path string
			if tc.flat {
				makeMKVFile(t, root, "Amelie (2001).mkv")
				path = filepath.Join(root, "Amelie (2001).mkv")
			} else {
				path = addEntry(t, root, "Amelie (2001)", "a.mkv")[0]
				addEntry(t, root, filepath.Join("Amelie (2001)", "old"), "Amelie.mkv")
				addEntry(t, root, "Amelie (2001) extras", "x.mkv")
			}
			writeSibling(t, movieRatingCachePath(), mustJSON(t, tc.cache(root, path)))
			before, _ := os.ReadFile(movieRatingCachePath())
			tmdb := titleTMDB{langs: map[string]string{"1": "en", "5": "de", "194": "fr"}}

			captureStdout(t, func() {
				origins, _ := fetchOrigins([]string{path}, root, "movie", tmdb, noPrompter{t})
				if len(origins) != 1 {
					t.Fatalf("origins = %v; want one", origins)
				}
				for _, o := range origins {
					if o.OrigLang != "fr" {
						t.Errorf("origin = %v; want fr from the file's ID 194", o)
					}
				}
			})

			if after, _ := os.ReadFile(movieRatingCachePath()); string(after) != string(before) {
				t.Errorf("cache rewritten:\n%s\nwant it left as\n%s", after, before)
			}
		})
	}
}

// Windows paths are case-insensitive, so a mapping under another spelling
// still counts.
func TestFetchOrigins_MappedFileMatchesAnySpelling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("paths are case-sensitive here")
	}
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{"files": {strings.ToUpper(paths[0]): "194"}}))
	tmdb := titleTMDB{langs: map[string]string{"194": "fr"}}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the mapped file", origins)
		}
	})
}

// assertCacheUnchanged fails if mkv-movie's cache no longer holds before.
func assertCacheUnchanged(t *testing.T, before []byte) {
	t.Helper()
	if after, _ := os.ReadFile(movieRatingCachePath()); string(after) != string(before) {
		t.Errorf("cache rewritten:\n%s\nwant it left as\n%s", after, before)
	}
}

// A title key isn't mapped to the files of a root holding more than one .mkv,
// subfolders included: they may be different movies.
func TestFetchOrigins_TitleKeyNotMappedToSeveralFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		nested string // a second .mkv in this subfolder, or "" for one beside the first
	}{
		{"two in the folder", ""},
		{"one in a subfolder", "Extras"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
			if tc.nested == "" {
				paths = append(paths, addEntry(t, root, "Amelie (2001)", "b.mkv")...)
			} else {
				addEntry(t, root, filepath.Join("Amelie (2001)", tc.nested), "b.mkv")
			}
			writeSibling(t, movieRatingCachePath(), `{"tmdb_ids":{"Amelie:2001":"194"}}`)
			before, _ := os.ReadFile(movieRatingCachePath())
			tmdb := titleTMDB{langs: map[string]string{"194": "fr"}}

			captureStdout(t, func() {
				if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
					t.Errorf("origins = %v; want fr from the title key", origins)
				}
			})
			assertCacheUnchanged(t, before)
		})
	}
}

// Subfolder files don't count when a file directly in the root is mapped: an
// extra mkv-movie mapped to another movie doesn't win a tie with the main
// file, even when the title key names the extra's ID. The title key's ID wins
// among the root's own IDs.
func TestFetchOrigins_RootFilesBeatExtras(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cache  func(main, extra string) map[string]map[string]string
		wantID string
	}{
		{"extra mapped to a lower ID", func(main, extra string) map[string]map[string]string {
			return map[string]map[string]string{"files": {main: "194", extra: "5"}}
		}, "194"},
		{"title key among the IDs", func(main, extra string) map[string]map[string]string {
			return map[string]map[string]string{"tmdb_ids": {"Amelie:2001": "194"}, "files": {main: "194", extra: "5"}}
		}, "194"},
		{"title key breaks a tie", func(main, extra string) map[string]map[string]string {
			return map[string]map[string]string{"tmdb_ids": {"Amelie:2001": "194"},
				"files": {main: "5", filepath.Join(filepath.Dir(main), "b.mkv"): "194"}}
		}, "194"},
		{"title key only through an extra", func(main, extra string) map[string]map[string]string {
			return map[string]map[string]string{"tmdb_ids": {"Amelie:2001": "194"}, "files": {main: "5", extra: "194"}}
		}, "5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv", "b.mkv")
			extra := addEntry(t, root, filepath.Join("Amelie (2001)", "Extras"), "Trailer.mkv")[0]
			writeSibling(t, movieRatingCachePath(), mustJSON(t, tc.cache(paths[0], extra)))
			before, _ := os.ReadFile(movieRatingCachePath())
			tmdb := titleTMDB{langs: map[string]string{"5": "en", "194": "fr"}}

			captureStdout(t, func() {
				origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t})
				if got, want := origins["Amelie (2001)"].OrigLang, tmdb.langs[tc.wantID]; got != want {
					t.Errorf("origin = %q; want %q from ID %s", got, want, tc.wantID)
				}
			})
			assertCacheUnchanged(t, before)
		})
	}
}

// A mapping for a file that no longer exists doesn't count.
func TestFetchOrigins_GoneFileMappingIgnored(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	dir := filepath.Dir(paths[0])
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{"files": {
		paths[0]: "194", filepath.Join(dir, "old1.mkv"): "5", filepath.Join(dir, "old2.mkv"): "5",
	}}))
	tmdb := titleTMDB{langs: map[string]string{"5": "en", "194": "fr"}}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the file that exists", origins)
		}
	})
}

// A folder whose movies are only in subfolders named without a year, mapped
// to different IDs, may be a grouping folder (A-M/<movie>) rather than a
// collection, so it is asked about rather than given one of their IDs.
func TestFetchOrigins_GroupingFolderAsked(t *testing.T) {
	root, _ := siblingLibrary(t, "A-M")
	amelie := addEntry(t, root, filepath.Join("A-M", "Amelie"), "a.mkv")
	coco := addEntry(t, root, filepath.Join("A-M", "Coco"), "c.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{"files": {amelie[0]: "194", coco[0]: "9"}}))
	tmdb := titleTMDB{langs: map[string]string{"9": "es", "194": "fr"}}
	p := &scriptPrompter{answers: []string{""}}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins(append(amelie, coco...), root, "movie", tmdb, p); len(origins) != 0 {
			t.Errorf("origins = %v; want none, the question was skipped", origins)
		}
	})
	if len(p.asked) != 1 {
		t.Errorf("asked %d times; want once", len(p.asked))
	}
}

// Each movie folder under a grouping folder, nested or not, is searched and
// asked about on its own and saved with its own files; with accept-all each
// takes its own top hit.
func TestFetchOrigins_GroupingFolderMoviesGetOwnOrigins(t *testing.T) {
	for _, tc := range []struct {
		name      string
		group     [2]string // the grouping folders holding Amelie and Coco
		answers   []string
		wantAsked int
	}{
		{"asked about each", [2]string{"A-M", "A-M"}, []string{"y", "y"}, 2},
		{"accept all", [2]string{"A-M", "A-M"}, []string{"a"}, 1},
		{"nested", [2]string{filepath.Join("A-M", "A"), filepath.Join("A-M", "C")}, []string{"y", "y"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setConfigDir(t, t.TempDir())
			root := t.TempDir()
			amelieDir, cocoDir := filepath.Join(tc.group[0], "Amelie (2001)"), filepath.Join(tc.group[1], "Coco (2017)")
			amelie := addEntry(t, root, amelieDir, "a.mkv")
			coco := addEntry(t, root, cocoDir, "c.mkv")
			tmdb := titleTMDB{ids: map[string]string{"Amelie": "42", "Coco": "9"}, langs: map[string]string{"42": "fr", "9": "es"}}
			p := &scriptPrompter{answers: tc.answers}

			captureStdout(t, func() {
				origins, roots := fetchOrigins(append(amelie, coco...), root, "movie", tmdb, p)
				if origins[amelieDir].OrigLang != "fr" || origins[cocoDir].OrigLang != "es" || len(origins) != 2 {
					t.Errorf("origins = %v; want %s fr and %s es", origins, amelieDir, cocoDir)
				}
				if roots[amelie[0]] != amelieDir || roots[coco[0]] != cocoDir {
					t.Errorf("roots = %v; want each file's movie folder", roots)
				}
			})

			if len(p.asked) != tc.wantAsked {
				t.Errorf("asked %d times; want %d:\n%s", len(p.asked), tc.wantAsked, strings.Join(p.asked, "\n"))
			}
			ids, files := readMovieSections(t)
			if ids["Amelie:2001"] != "42" || ids["Coco:2017"] != "9" || len(ids) != 2 {
				t.Errorf("tmdb_ids = %v; want Amelie:2001 → 42 and Coco:2017 → 9", ids)
			}
			if files[amelie[0]] != "42" || files[coco[0]] != "9" || len(files) != 2 {
				t.Errorf("files = %v; want each movie's file → its own ID", files)
			}
		})
	}
}

// A movie under a grouping folder that mkv-movie has processed keeps its own
// ID; it isn't used for the movie beside it, which is asked about and saved.
func TestFetchOrigins_GroupingFolderOneMovieMapped(t *testing.T) {
	root, _ := siblingLibrary(t, "A-M")
	amelie := addEntry(t, root, filepath.Join("A-M", "Amelie (2001)"), "a.mkv")
	coco := addEntry(t, root, filepath.Join("A-M", "Coco (2017)"), "c.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{"files": {amelie[0]: "194"}}))
	tmdb := titleTMDB{ids: map[string]string{"Coco": "9"}, langs: map[string]string{"9": "es", "194": "fr"}}
	p := &scriptPrompter{answers: []string{"y"}}

	captureStdout(t, func() {
		origins, _ := fetchOrigins(append(amelie, coco...), root, "movie", tmdb, p)
		if origins[filepath.Join("A-M", "Amelie (2001)")].OrigLang != "fr" || origins[filepath.Join("A-M", "Coco (2017)")].OrigLang != "es" {
			t.Errorf("origins = %v; want Amelie fr from its mapping and Coco es from the answer", origins)
		}
	})

	if len(p.asked) != 1 || !strings.Contains(p.asked[0], "Coco") {
		t.Errorf("asked %q; want one question, about Coco", p.asked)
	}
	ids, files := readMovieSections(t)
	if ids["Coco:2017"] != "9" || len(ids) != 1 || files[amelie[0]] != "194" || files[coco[0]] != "9" || len(files) != 2 {
		t.Errorf("tmdb_ids = %v, files = %v; want Coco saved with its file and Amelie's mapping kept", ids, files)
	}
}

// A movie folder under a grouping folder follows the rules of any movie
// folder: one split across discs is saved with both discs' files, and a flat
// collection is used for the run only.
func TestFetchOrigins_GroupingFolderMovieFolderRules(t *testing.T) {
	root, _ := siblingLibrary(t, "A-M")
	discs := addEntry(t, root, filepath.Join("A-M", "Amelie (2001)", "Disc1"), "a.mkv")
	discs = append(discs, addEntry(t, root, filepath.Join("A-M", "Amelie (2001)", "Disc2"), "b.mkv")...)
	collection := addEntry(t, root, filepath.Join("A-M", "Kill Bill Collection"), "v1.mkv", "v2.mkv")
	tmdb := titleTMDB{ids: map[string]string{"Amelie": "42", "Kill Bill Collection": "24"}, langs: map[string]string{"42": "fr", "24": "en"}}

	out := captureStdout(t, func() {
		origins, _ := fetchOrigins(append(discs, collection...), root, "movie", tmdb, &scriptPrompter{answers: []string{"y", "y"}})
		if origins[filepath.Join("A-M", "Amelie (2001)")].OrigLang != "fr" || origins[filepath.Join("A-M", "Kill Bill Collection")].OrigLang != "en" {
			t.Errorf("origins = %v; want Amelie fr and the collection en", origins)
		}
	})

	if want := filepath.Join("A-M", "Kill Bill Collection") + ": id 24 used for this run only"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	ids, files := readMovieSections(t)
	if !maps.Equal(ids, map[string]string{"Amelie:2001": "42"}) || !maps.Equal(files, map[string]string{discs[0]: "42", discs[1]: "42"}) {
		t.Errorf("tmdb_ids = %v, files = %v; want only Amelie saved, with both discs", ids, files)
	}
}

// contentRoots goes below the first folder only for a movie library's
// grouping folders.
func TestContentRoots(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) string {
		makeMKVFile(t, mkdir(t, filepath.Join(root, filepath.Dir(rel))), filepath.Base(rel))
		return filepath.Join(root, rel)
	}
	group := []string{mk("A-M/Amelie (2001)/a.mkv"), mk("A-M/Coco (2017)/c.mkv")}
	loose := []string{mk("Misc/loose.mkv"), mk("Misc/Amelie (2001)/a.mkv"), mk("Misc/Coco (2017)/c.mkv")}
	flat := mk("Brave (2012).mkv")
	outside := filepath.Join(t.TempDir(), "x.mkv")
	files := slices.Concat(group, loose, []string{flat, outside})

	for _, tc := range []struct {
		libType string
		want    []string // per file, "" for none
	}{
		{"movie", []string{filepath.Join("A-M", "Amelie (2001)"), filepath.Join("A-M", "Coco (2017)"), "Misc", "Misc", "Misc", "Brave (2012).mkv", ""}},
		{"tv", []string{"A-M", "A-M", "Misc", "Misc", "Misc", "Brave (2012).mkv", ""}},
	} {
		t.Run(tc.libType, func(t *testing.T) {
			roots := contentRoots(files, root, tc.libType)
			for i, f := range files {
				if roots[f] != tc.want[i] {
					t.Errorf("root of %s = %q; want %q", f, roots[f], tc.want[i])
				}
			}
		})
	}
}

// mkdir makes dir and its parents, and returns it.
func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An ID TMDB no longer has falls back to the next: the next most mapped, then
// the title key.
func TestFetchOrigins_MissingIDFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cache func(p []string) map[string]map[string]string
	}{
		{"next mapped ID", func(p []string) map[string]map[string]string {
			return map[string]map[string]string{"files": {p[0]: "5", p[1]: "5", p[2]: "194"}}
		}},
		{"title key", func(p []string) map[string]map[string]string {
			return map[string]map[string]string{"tmdb_ids": {"Amelie:2001": "194"}, "files": {p[0]: "5"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv", "b.mkv", "c.mkv")
			writeSibling(t, movieRatingCachePath(), mustJSON(t, tc.cache(paths)))
			before, _ := os.ReadFile(movieRatingCachePath())
			tmdb := titleTMDB{langs: map[string]string{"194": "fr"}} // no 5

			out := captureStdout(t, func() {
				if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
					t.Errorf("origins = %v; want fr from ID 194", origins)
				}
			})
			if want := "no original language from TMDB for id 5, trying id 194"; !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
			assertCacheUnchanged(t, before)
		})
	}
}

// A file mapped to "" by hand isn't mapped over, and the cache isn't
// rewritten on every run for it.
func TestFetchOrigins_EmptyMappingNotRewritten(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{
		"tmdb_ids": {"Amelie:2001": "194"}, "files": {paths[0]: ""},
	}))
	before, _ := os.ReadFile(movieRatingCachePath())
	tmdb := titleTMDB{langs: map[string]string{"194": "fr"}}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the title key", origins)
		}
	})
	assertCacheUnchanged(t, before)
}

// A title-key movie whose file mapping can't be saved isn't called lost: its
// title key is still in the cache, so the next run asks nothing. A cache
// that turned corrupt can't be loaded next run either, so then it is lost.
func TestFetchOrigins_BackfillSaveFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		breakIt  func(t *testing.T, path string)
		wantLost bool
	}{
		{"cache unreadable", func(t *testing.T, path string) {
			// A directory where the cache was: reading it fails, but not as corrupt.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"cache corrupt", func(t *testing.T, path string) { writeSibling(t, path, `{"tmdb_ids":`) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
			writeSibling(t, movieRatingCachePath(), `{"tmdb_ids":{"Amelie:2001":"194"}}`)
			tmdb := titleTMDB{langs: map[string]string{"194": "fr"},
				onOrigin: func(string) { tc.breakIt(t, movieRatingCachePath()) }}
			old := beforeFinalSiblingSave
			beforeFinalSiblingSave = func() {}
			t.Cleanup(func() { beforeFinalSiblingSave = old })

			out := captureStdout(t, func() { fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}) })

			lost := strings.Contains(out, "lost, and the next run asks for them again: Amelie (2001) (id 194)")
			unmapped := strings.Contains(out, "files aren't mapped to their IDs yet; the next run maps them, unless mkv-movie --clean drops the IDs first: Amelie (2001) (id 194)")
			if lost != tc.wantLost || unmapped == tc.wantLost {
				t.Errorf("lost = %v, unmapped = %v; want lost = %v:\n%s", lost, unmapped, tc.wantLost, out)
			}
		})
	}
}

// A root holding several movies that is asked about uses the answer for this
// run only: its title key isn't saved, nor are other movies' files mapped to
// it. An unprocessed movie with its extras is saved as before.
func TestFetchOrigins_AnswerForSeveralMoviesNotSaved(t *testing.T) {
	for _, tc := range []struct {
		name      string
		layout    func(t *testing.T, root string) []string
		cache     func(paths []string) string
		wantSaved bool
	}{
		{"mapped files disagree", func(t *testing.T, root string) []string {
			p := addEntry(t, root, filepath.Join("A-M", "Amelie"), "a.mkv")
			p = append(p, addEntry(t, root, filepath.Join("A-M", "Brave"), "b.mkv")...)
			return append(p, addEntry(t, root, filepath.Join("A-M", "Coco"), "c.mkv")...)
		}, func(p []string) string {
			return `{"files":{` + strings.Join([]string{jsonPair(p[0], "194"), jsonPair(p[2], "9")}, ",") + `}}`
		}, false},
		{"loose file beside mapped movie folders", func(t *testing.T, root string) []string {
			p := addEntry(t, root, "A-M", "loose.mkv")
			p = append(p, addEntry(t, root, filepath.Join("A-M", "Amelie (2001)"), "a.mkv")...)
			return append(p, addEntry(t, root, filepath.Join("A-M", "Coco (2017)"), "c.mkv")...)
		}, func(p []string) string {
			return `{"files":{` + strings.Join([]string{jsonPair(p[1], "194"), jsonPair(p[2], "9")}, ",") + `}}`
		}, false},
		{"unprocessed movie folders", func(t *testing.T, root string) []string {
			p := addEntry(t, root, filepath.Join("A-M", "Amelie"), "a.mkv")
			return append(p, addEntry(t, root, filepath.Join("A-M", "Brave"), "b.mkv")...)
		}, func([]string) string { return `{"movies":{"9":"M"}}` }, false},
		{"discs", func(t *testing.T, root string) []string {
			p := addEntry(t, root, filepath.Join("A-M", "Disc1"), "a.mkv")
			return append(p, addEntry(t, root, filepath.Join("A-M", "Disc2"), "b.mkv")...)
		}, func([]string) string { return `{"movies":{"9":"M"}}` }, false},
		{"movie with extras", func(t *testing.T, root string) []string {
			p := addEntry(t, root, "A-M", "movie.mkv")
			return append(p, addEntry(t, root, filepath.Join("A-M", "Extras"), "trailer.mkv")...)
		}, func([]string) string { return `{"movies":{"9":"M"}}` }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setConfigDir(t, t.TempDir())
			root := t.TempDir()
			paths := tc.layout(t, root)
			writeSibling(t, movieRatingCachePath(), tc.cache(paths))
			before, _ := os.ReadFile(movieRatingCachePath())

			out := captureStdout(t, func() {
				if origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, &scriptPrompter{answers: []string{"y"}}); origins["A-M"].OrigLang != "fr" {
					t.Errorf("origins = %v; want fr from the confirmed answer", origins)
				}
			})

			if !tc.wantSaved {
				assertCacheUnchanged(t, before)
				if !strings.Contains(out, "used for this run only") {
					t.Errorf("output doesn't say the answer isn't saved:\n%s", out)
				}
				return
			}
			ids, files := readMovieSections(t)
			if ids["A-M:"] != "42" || files[paths[0]] != "42" || files[paths[1]] != "42" {
				t.Errorf("tmdb_ids = %v, files = %v; want the answer saved for the movie and its extra", ids, files)
			}
		})
	}
}

// jsonPair returns "key":"value" as JSON.
func jsonPair(k, v string) string {
	kb, _ := json.Marshal(k)
	vb, _ := json.Marshal(v)
	return string(kb) + ":" + string(vb)
}

// A title key stored typed ("0194") is left as it is, and the file is mapped
// to that same value, so mkv-movie's rating under it stays reachable.
func TestFetchOrigins_BackfillKeepsTypedID(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv")
	writeSibling(t, movieRatingCachePath(), `{"movies":{"0194":"M"},"tmdb_ids":{"Amelie:2001":"0194"}}`)
	var looked []string
	tmdb := titleTMDB{langs: map[string]string{"194": "fr"}, onOrigin: func(id string) { looked = append(looked, id) }}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr", origins)
		}
	})

	if !slices.Equal(looked, []string{"194"}) {
		t.Errorf("looked up %q; want the normalized 194", looked)
	}
	ids, files := readMovieSections(t)
	if ids["Amelie:2001"] != "0194" || len(files) != 1 || files[paths[0]] != "0194" {
		t.Errorf("tmdb_ids = %v, files = %v; want the key kept as 0194 and the file → 0194", ids, files)
	}
}

// A TMDB error other than not-found isn't a missing ID: nothing falls back,
// the error is named, and the cache is left alone.
func TestFetchOrigins_TMDBErrorDoesNotFallBack(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv", "b.mkv", "c.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{"files": {paths[0]: "5", paths[1]: "5", paths[2]: "194"}}))
	before, _ := os.ReadFile(movieRatingCachePath())
	var looked []string
	tmdb := titleTMDB{langs: map[string]string{"5": "", "194": "fr"}, onOrigin: func(id string) { looked = append(looked, id) }}

	out := captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); len(origins) != 0 {
			t.Errorf("origins = %v; want none after the error", origins)
		}
	})

	if !slices.Equal(looked, []string{"5"}) {
		t.Errorf("looked up %q; want only 5", looked)
	}
	if !strings.Contains(out, "TMDB is down") {
		t.Errorf("output doesn't name the error:\n%s", out)
	}
	assertCacheUnchanged(t, before)
}

// When TMDB has none of the IDs, the root has no match and nothing is saved.
func TestFetchOrigins_AllIDsMissing(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv", "b.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{
		"tmdb_ids": {"Amelie:2001": "7"}, "files": {paths[0]: "5", paths[1]: "6"},
	}))
	before, _ := os.ReadFile(movieRatingCachePath())

	out := captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", titleTMDB{}, noPrompter{t}); len(origins) != 0 {
			t.Errorf("origins = %v; want none", origins)
		}
	})

	// The last ID tried is named, with the cache it came from.
	want := "Amelie (2001): no original language from TMDB for movie id 7 (from mkv-movie's cache, movie_rating_cache.json); fix or remove it there"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	assertCacheUnchanged(t, before)
}

// A movie split across subfolders (discs) is one movie when its folder name
// gives a year: the answer is saved, and the next run asks nothing.
func TestFetchOrigins_MultiDiscMovieSaved(t *testing.T) {
	root, _ := siblingLibrary(t, "Amelie (2001)")
	paths := addEntry(t, root, filepath.Join("Amelie (2001)", "Disc1"), "a.mkv")
	paths = append(paths, addEntry(t, root, filepath.Join("Amelie (2001)", "Disc2"), "b.mkv")...)

	captureStdout(t, func() { fetchOrigins(paths, root, "movie", searchTMDB{}, &scriptPrompter{answers: []string{"y"}}) })

	ids, files := readMovieSections(t)
	if ids["Amelie:2001"] != "42" || files[paths[0]] != "42" || files[paths[1]] != "42" {
		t.Errorf("tmdb_ids = %v, files = %v; want the answer saved for both discs", ids, files)
	}
	captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("run 2 origins = %v; want fr without asking", origins)
		}
	})
}

// Several unmapped .mkv files directly in a folder may be different movies,
// so none is mapped to the answer. Without a year in its name the answer is
// for this run only; with one, the title key is saved and the next run uses
// it without asking, still mapping no file.
func TestFetchOrigins_FlatCollectionFilesNotMapped(t *testing.T) {
	for _, tc := range []struct {
		entry, wantOut string
		wantIDs        map[string]string
	}{
		{"Kill Bill Collection", "used for this run only", nil},
		{"Amelie (2001)", "not for its files", map[string]string{"Amelie:2001": "42"}},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			root, paths := siblingLibrary(t, tc.entry, "a.mkv", "b.mkv")
			paths = append(paths, addEntry(t, root, filepath.Join(tc.entry, "Extras"), "trailer.mkv")...)
			writeSibling(t, movieRatingCachePath(), `{"movies":{"9":"M"}}`)
			before, _ := os.ReadFile(movieRatingCachePath())

			out := captureStdout(t, func() {
				if origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, &scriptPrompter{answers: []string{"y"}}); origins[tc.entry].OrigLang != "fr" {
					t.Errorf("origins = %v; want fr from the confirmed answer", origins)
				}
			})
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output lacks %q:\n%s", tc.wantOut, out)
			}

			if tc.wantIDs == nil {
				assertCacheUnchanged(t, before)
				return
			}
			ids, files := readMovieSections(t)
			if !maps.Equal(ids, tc.wantIDs) || len(files) != 0 {
				t.Errorf("tmdb_ids = %v, files = %v; want only %v and no file mapped", ids, files, tc.wantIDs)
			}
			saved, _ := os.ReadFile(movieRatingCachePath())
			captureStdout(t, func() {
				if origins, _ := fetchOrigins(paths, root, "movie", searchTMDB{}, noPrompter{t}); origins[tc.entry].OrigLang != "fr" {
					t.Errorf("run 2 origins = %v; want fr from the title key without asking", origins)
				}
			})
			assertCacheUnchanged(t, saved)
		})
	}
}

// Files only in subfolders that agree on one ID give that ID without asking.
func TestFetchOrigins_SingleNestedIDUsed(t *testing.T) {
	root, _ := siblingLibrary(t, "Amelie (2001)")
	paths := addEntry(t, root, filepath.Join("Amelie (2001)", "Disc 1"), "a.mkv", "b.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{"files": {paths[0]: "194", paths[1]: "194"}}))
	before, _ := os.ReadFile(movieRatingCachePath())
	tmdb := titleTMDB{langs: map[string]string{"194": "fr"}}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the subfolder files' ID", origins)
		}
	})
	assertCacheUnchanged(t, before)
}

// A mapping on a drive that isn't reachable (offline) still counts.
func TestFetchOrigins_OfflineDriveMappingCounts(t *testing.T) {
	drive := unusedDrive(t)
	setConfigDir(t, t.TempDir())
	root := drive + `\Movies`
	path := filepath.Join(root, "Amelie (2001)", "a.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{"files": {path: "194"}}))
	tmdb := titleTMDB{langs: map[string]string{"194": "fr"}}

	captureStdout(t, func() {
		if origins, _ := fetchOrigins([]string{path}, root, "movie", tmdb, noPrompter{t}); origins["Amelie (2001)"].OrigLang != "fr" {
			t.Errorf("origins = %v; want fr from the offline file's ID", origins)
		}
	})
}

func TestPathGone(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "here.mkv")
	if pathGone(filepath.Join(dir, "here.mkv")) {
		t.Error("an existing file is gone")
	}
	if !pathGone(filepath.Join(dir, "missing.mkv")) {
		t.Error("a missing file on a reachable volume isn't gone")
	}
	if runtime.GOOS == "windows" {
		if d := unusedDrive(t); pathGone(d + `\Movies\a.mkv`) {
			t.Error("a file on an unreachable drive is gone")
		}
	}
}

// A folder named without a year beside the movies under a grouping folder
// (Extras, a disc) is looked up for the run only: nothing is saved for it.
func TestFetchOrigins_GroupingFolderNoYearSubfolderNotSaved(t *testing.T) {
	root, _ := siblingLibrary(t, "Kill Bill Collection")
	vol1Dir := filepath.Join("Kill Bill Collection", "Kill Bill Vol 1 (2003)")
	extrasDir := filepath.Join("Kill Bill Collection", "Extras")
	vol1 := addEntry(t, root, vol1Dir, "a.mkv")
	extras := addEntry(t, root, extrasDir, "making-of.mkv")
	tmdb := titleTMDB{
		ids:   map[string]string{"Kill Bill Vol 1": "24", "Extras": "999"},
		langs: map[string]string{"24": "en", "999": "ja"},
	}

	captureStdout(t, func() {
		origins, _ := fetchOrigins(append(vol1, extras...), root, "movie", tmdb, &scriptPrompter{answers: []string{"a"}})
		if origins[vol1Dir].OrigLang != "en" || origins[extrasDir].OrigLang != "ja" {
			t.Errorf("origins = %v; want each folder's answer for the run", origins)
		}
	})

	ids, files := readMovieSections(t)
	if len(ids) != 1 || ids["Kill Bill Vol 1:2003"] != "24" {
		t.Errorf("tmdb_ids = %v; want only Kill Bill Vol 1:2003 → 24", ids)
	}
	if len(files) != 1 || files[vol1[0]] != "24" {
		t.Errorf("files = %v; want only the movie's file → 24", files)
	}
}
