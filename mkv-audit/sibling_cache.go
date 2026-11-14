package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// siblingCache describes one sibling tool's cache, for loading and messages.
type siblingCache struct {
	name  string // "mkv-movie's cache"
	items string // what it holds IDs for: "movie"
	// trailingOK matches how the owner parses the file: mkv-tvshow decodes the
	// first JSON value and ignores the rest, mkv-movie unmarshals the whole file.
	trailingOK bool
	// ifDeleted says what deleting the cache costs, for the corrupt-file hint.
	ifDeleted string
	path      func() (string, error)
}

var (
	tvShowSibling = siblingCache{
		name:       "mkv-tvshow's cache",
		items:      "show",
		trailingOK: true,
		ifDeleted: "If you delete it instead, run mkv-tvshow over your whole library to rebuild it before any mkv-tvshow --clean,\n" +
			"       which prunes rating_cache.json and missing_ignore.json to the shows it lists.",
		path: siblingCachePath("mkv-tvshow", "show_cache.json"),
	}
	movieSibling = siblingCache{
		name:      "mkv-movie's cache",
		items:     "movie",
		ifDeleted: "If you delete it instead, mkv-movie asks for every rating again.",
		path:      siblingCachePath("mkv-movie", "movie_rating_cache.json"),
	}
)

// siblingCachePath returns a func giving the path to tool's file in its
// config dir, found as the tool finds it: os.UserConfigDir()/<tool>.
func siblingCachePath(tool, file string) func() (string, error) {
	return func() (string, error) {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, tool, file), nil
	}
}

// errAbs marks a file whose absolute path couldn't be worked out.
var errAbs = errors.New("can't resolve path")

// tvEntry is a confirmed show ID to save to mkv-tvshow's cache.
type tvEntry struct{ dir, absPath, id string }

// movieEntry is a confirmed movie ID to save to mkv-movie's cache. files
// are the .mkv files to map to it; none for a collection.
type movieEntry struct {
	dir, title, year, id string
	files                []string
}

// saveTVShowCacheEntries adds or updates entries in mkv-tvshow's
// show_cache.json at path, keyed by the show folder's absolute path
// (mkv-tvshow's format). A key already there under another spelling of the
// path (Windows) is updated in place rather than duplicated.
func saveTVShowCacheEntries(path string, entries []tvEntry) error {
	m, _, err := readSiblingJSON(path, tvShowSibling.trailingOK)
	if err != nil {
		return err
	}
	keys := make(map[string]string, len(m)) // pathKey → key as spelled in the file
	for k := range m {
		if filepath.IsAbs(k) {
			keys[pathKey(k)] = k
		}
	}
	for _, e := range entries {
		key, ok := keys[pathKey(e.absPath)]
		if !ok {
			key = e.absPath
			keys[pathKey(key)] = key
		}
		if m[key], err = json.Marshal(e.id); err != nil {
			return err
		}
	}
	return writeSiblingJSON(path, m)
}

// saveMovieTMDBIDEntries adds or updates the tmdb_ids entries in mkv-movie's
// movie_rating_cache.json at path, keyed "title:YEAR" (mkv-movie's format),
// and maps each movie's .mkv files to its ID. Without a files entry,
// mkv-movie --clean prunes the ID. A file mkv-movie already maps, under any
// spelling or in legacy meta_applied, keeps its ID: mkv-movie --clean would
// otherwise drop the old ID's rating and Plex state.
func saveMovieTMDBIDEntries(path string, entries []movieEntry) error {
	raw, shape, err := readMovieCache(path)
	if err != nil {
		return err
	}
	ids, byFile := shape.TMDBIDs, shape.Files
	if ids == nil {
		ids = map[string]string{}
	}
	if byFile == nil {
		byFile = map[string]string{}
	}
	mapped := shape.fileView()
	for _, e := range entries {
		ids[e.title+":"+e.year] = e.id
		for _, f := range e.files {
			if !isMKV(f) { // mkv-movie only walks .mkv files
				continue
			}
			abs, err := filepath.Abs(f)
			if err != nil {
				return fmt.Errorf("%w %s: %v", errAbs, f, err)
			}
			if _, ok := mapped[pathKey(abs)]; !ok {
				byFile[abs] = e.id
				mapped[pathKey(abs)] = e.id
			}
		}
	}
	for key, section := range map[string]map[string]string{"tmdb_ids": ids, "files": byFile} {
		if raw[key], err = json.Marshal(section); err != nil {
			return err
		}
	}
	return writeSiblingJSON(path, raw)
}

func isMKV(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".mkv")
}

// movieCacheShape mirrors the types of mkv-movie's movieRatingCacheData, so
// mkv-audit calls the cache bad exactly when mkv-movie would refuse to load it.
type movieCacheShape struct {
	Movies         map[string]string `json:"movies"`
	Applied        map[string]string `json:"applied"`
	TMDBIDs        map[string]string `json:"tmdb_ids"`
	LegacyMeta     map[string]string `json:"meta_applied"`
	PlexRatings    map[string]string `json:"plex_ratings"`
	StreamsApplied map[string]string `json:"streams_applied"`
	Files          map[string]string `json:"files"`
}

// fileView maps pathKey(file) → ID for every file mkv-movie has mapped, with
// legacy meta_applied back-filled as mkv-movie's loader does. It is only
// read: meta_applied is never written back.
func (s movieCacheShape) fileView() map[string]string {
	v := make(map[string]string, len(s.Files)+len(s.LegacyMeta))
	for f, id := range s.Files {
		v[pathKey(f)] = id
	}
	for f, id := range s.LegacyMeta {
		if _, ok := v[pathKey(f)]; !ok && id != "" {
			v[pathKey(f)] = id
		}
	}
	return v
}

// readMovieCache reads mkv-movie's cache, keeping every key as it is in raw.
func readMovieCache(path string) (raw map[string]json.RawMessage, shape movieCacheShape, err error) {
	raw, b, err := readSiblingJSON(path, movieSibling.trailingOK)
	if err != nil || b == nil {
		return raw, shape, err
	}
	if err := json.Unmarshal(b, &shape); err != nil {
		return nil, shape, fmt.Errorf("parse %s: %w", path, err)
	}
	return raw, shape, nil
}

// readSiblingJSON reads a sibling tool's JSON object, keeping every key as it
// is, and returns the file's bytes (nil when it doesn't exist). A missing
// file is empty; one that can't be read or parsed is an error, so it is never
// overwritten (the owning tool refuses to load it, and the user can still
// restore it).
func readSiblingJSON(path string, trailingOK bool) (map[string]json.RawMessage, []byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var raw map[string]json.RawMessage
	if trailingOK {
		err = json.NewDecoder(bytes.NewReader(b)).Decode(&raw)
	} else {
		err = json.Unmarshal(b, &raw)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if raw == nil { // the file holds null
		raw = map[string]json.RawMessage{}
	}
	return raw, b, nil
}

// isCorrupt reports whether err means a sibling cache's contents can't be
// parsed, rather than that the file couldn't be read.
func isCorrupt(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// writeSiblingJSON writes m to path atomically, creating its directory.
func writeSiblingJSON(path string, m map[string]json.RawMessage) error {
	b, err := json.MarshalIndent(m, "", "    ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return atomicWriteFile(path, b)
}

// loadTVShowCache reads mkv-tvshow's show_cache.json at path: folder (absolute
// path, or a legacy bare name) → TMDB ID, as strings or numbers. A missing
// file is an empty cache; one that can't be read or parsed is an error.
func loadTVShowCache(path string) (map[string]string, error) {
	raw, _, err := readSiblingJSON(path, tvShowSibling.trailingOK)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(raw))
	for k, b := range raw {
		// The Python-generated cache may store IDs as numbers; other value
		// types are skipped, as mkv-tvshow does.
		var v any
		if json.Unmarshal(b, &v) != nil {
			continue
		}
		switch val := v.(type) {
		case string:
			m[k] = val
		case float64:
			m[k] = fmt.Sprintf("%d", int(val))
		}
	}
	return m, nil
}

// siblingTMDBCaches holds the cache of the sibling tool for this library type.
type siblingTMDBCaches struct {
	tvShowIDs  map[string]string // folder name or absolute path, as keyed in the file → tmdbID
	tvPathIDs  map[string]string // pathKey(absolute path) → tmdbID
	movieIDs   map[string]string // "title:YEAR" → tmdbID
	movieFiles map[string]string // pathKey(file) → tmdbID
	tv         *siblingSaver[tvEntry]
	movie      *siblingSaver[movieEntry]
}

// loadSiblingCaches reads the cache of the tool that owns libType's library
// ("tv" or "movie"). One that can't be found or loaded is reported once: the
// run goes on, but nothing is saved to it.
func loadSiblingCaches(libType string) *siblingTMDBCaches {
	sc := &siblingTMDBCaches{}
	switch libType {
	case "tv":
		sc.tv = &siblingSaver[tvEntry]{cache: tvShowSibling, save: saveTVShowCacheEntries,
			describe: func(e tvEntry) string { return fmt.Sprintf("%s (id %s)", e.dir, e.id) }}
		sc.tv.open(func(path string) (err error) {
			if sc.tvShowIDs, err = loadTVShowCache(path); err != nil {
				return err
			}
			sc.tvPathIDs = make(map[string]string, len(sc.tvShowIDs))
			for k, id := range sc.tvShowIDs {
				if filepath.IsAbs(k) && id != "" {
					sc.tvPathIDs[pathKey(k)] = id
				}
			}
			return nil
		})
	case "movie":
		sc.movie = &siblingSaver[movieEntry]{cache: movieSibling, save: saveMovieTMDBIDEntries,
			describe: func(e movieEntry) string { return fmt.Sprintf("%s (id %s)", e.dir, e.id) }}
		sc.movie.open(func(path string) error {
			_, shape, err := readMovieCache(path)
			sc.movieIDs, sc.movieFiles = shape.TMDBIDs, shape.fileView()
			return err
		})
	}
	return sc
}

// rememberTV records a confirmed show ID, for this run and in mkv-tvshow's cache.
func (sc *siblingTMDBCaches) rememberTV(dir, absPath, id string) {
	if sc.tvPathIDs == nil {
		sc.tvPathIDs = map[string]string{}
	}
	sc.tvPathIDs[pathKey(absPath)] = id
	sc.tv.add(tvEntry{dir, absPath, id})
}

// rememberMovie records a confirmed movie ID for the content root at root,
// for this run and in mkv-movie's cache. In a collection (mkv-movie maps
// files under root to another movie) only the title key is saved: the
// root's other files are other movies, so none is mapped to id.
func (sc *siblingTMDBCaches) rememberMovie(dir, root, title, year, id string, files []string) {
	for other := range sc.rootMovieIDs(root) {
		if other != id {
			files = nil
		}
	}
	if sc.movieIDs == nil {
		sc.movieIDs = map[string]string{}
	}
	if sc.movieFiles == nil {
		sc.movieFiles = map[string]string{}
	}
	sc.movieIDs[title+":"+year] = id
	for _, f := range files {
		if k := pathKey(f); isMKV(f) && sc.movieFiles[k] == "" {
			sc.movieFiles[k] = id
		}
	}
	sc.movie.add(movieEntry{dir, title, year, id, files})
}

// finish retries any save that failed.
func (sc *siblingTMDBCaches) finish() {
	sc.tv.finish()
	sc.movie.finish()
}

// lookupTV returns the TMDB show ID for the given folder name, or "".
// It checks both the bare folder name (legacy format) and the absolute path
// (current mkv-tvshow format: filepath.Join(libraryRoot, folderName)), the
// latter under any spelling of the path on Windows.
func (sc *siblingTMDBCaches) lookupTV(folderName, libraryRoot string) string {
	if id := sc.tvShowIDs[folderName]; id != "" {
		return id
	}
	if libraryRoot != "" {
		return sc.tvPathIDs[pathKey(filepath.Join(libraryRoot, folderName))]
	}
	return ""
}

// lookupMovie returns the TMDB movie ID for the content root at root (a
// folder, or the file itself in a flat library), or "". The ID mkv-movie
// recorded for the files under root comes first, used only when they all
// agree. Failing that, as for a collection (its files are different movies),
// the "title:YEAR" key is tried ("title:" when year is empty). mkv-movie
// builds its titles differently, so that key is mostly mkv-audit's own.
func (sc *siblingTMDBCaches) lookupMovie(title, year, root string) string {
	if ids := sc.rootMovieIDs(root); len(ids) == 1 {
		for id := range ids {
			return id
		}
	}
	return sc.movieIDs[title+":"+year]
}

// rootMovieIDs returns the IDs mkv-movie maps the files under root to.
func (sc *siblingTMDBCaches) rootMovieIDs(root string) map[string]bool {
	rootKey := pathKey(root)
	prefix := rootKey + string(filepath.Separator)
	ids := map[string]bool{}
	for k, v := range sc.movieFiles {
		if v != "" && (k == rootKey || strings.HasPrefix(k, prefix)) {
			ids[v] = true
		}
	}
	return ids
}

// siblingSaver saves confirmed IDs to one sibling cache as they come. A
// failed save is reported once and kept pending: each later confirmation
// retries it, and finish tries once more.
type siblingSaver[E any] struct {
	cache    siblingCache
	off      bool   // the cache couldn't be found or loaded
	path     string // where the cache is, once found
	pending  []E    // confirmed but not yet saved
	err      error  // the last save's error
	save     func(path string, entries []E) error
	describe func(E) string
}

// open finds the cache and loads it with load, turning saving off with one
// warning when either fails.
func (s *siblingSaver[E]) open(load func(path string) error) {
	path, err := s.cache.path()
	if err != nil {
		fmt.Printf("[WARN] %s can't be found (%v): every %s is searched on TMDB and asked about this run, and nothing is saved to it.\n",
			s.cache.name, err, s.cache.items)
		s.off = true
		return
	}
	s.path = path
	if err := load(path); err != nil {
		hint := "Close any program that has it open, then run mkv-audit again."
		if isCorrupt(err) {
			hint = "It is corrupt and was left as it is: restore it from a backup if you have one.\n       " + s.cache.ifDeleted
		}
		fmt.Printf("[WARN] %s can't be loaded: %v\n"+
			"       Every %s is searched on TMDB and asked about this run, and the IDs confirmed aren't saved to it.\n"+
			"       %s\n", s.cache.name, err, s.cache.items, hint)
		s.off = true
	}
}

func (s *siblingSaver[E]) add(e E) {
	if s == nil || s.off {
		return
	}
	s.pending = append(s.pending, e)
	err := s.save(s.path, s.pending)
	switch {
	case err != nil && s.err == nil:
		fmt.Printf("[WARN] %s: could not save to %s: %v\n"+
			"       This run still uses the ID, and the save is retried as the run goes on.%s\n",
			s.describe(e), s.cache.name, err, saveHint(err))
	case err == nil && s.err != nil:
		fmt.Printf("[TMDB]   %s saved.\n", s.cache.name)
	}
	if err == nil {
		s.pending = nil
	}
	s.err = err
}

// finish retries a failed save once, and says what was lost if it fails again.
func (s *siblingSaver[E]) finish() {
	if s == nil || s.off || len(s.pending) == 0 {
		return
	}
	// One more try: on Windows the rename fails while another program
	// (antivirus, an indexer) briefly holds the cache open.
	beforeFinalSiblingSave()
	err := s.save(s.path, s.pending)
	if err == nil {
		fmt.Printf("[TMDB]   %s saved.\n", s.cache.name)
		s.pending = nil
		return
	}
	lost := make([]string, len(s.pending))
	for i, e := range s.pending {
		lost[i] = s.describe(e)
	}
	fmt.Printf("[WARN] %s still can't be saved, so these TMDB IDs are lost and the next run asks for them again: %s\n       %v%s\n",
		s.cache.name, strings.Join(lost, ", "), err, saveHint(err))
}

// beforeFinalSiblingSave runs before siblingSaver.finish's last save attempt; tests replace it.
var beforeFinalSiblingSave = func() { time.Sleep(time.Second) }

// saveHint suggests a fix for a failed sibling-cache save, on a new line.
func saveHint(err error) string {
	switch {
	case isCorrupt(err):
		return "\n       It changed since mkv-audit loaded it and can't be parsed now, so it was left as it is."
	case errors.Is(err, errAbs):
		return ""
	case errors.Is(err, fs.ErrPermission):
		return "\n       Close any program that has it open."
	}
	return ""
}
