package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// siblingCache describes one sibling tool's cache, for loading and messages.
type siblingCache struct {
	name string // "mkv-movie's cache"
	// trailingOK matches how the owner parses the file: mkv-tvshow decodes the
	// first JSON value and ignores the rest, mkv-movie unmarshals the whole file.
	trailingOK bool
	// lost says what deleting the cache costs, for the corrupt-file hint.
	lost string
	path func() string
}

var (
	tvShowSibling = siblingCache{
		name:       "mkv-tvshow's cache",
		trailingOK: true,
		lost:       "mkv-tvshow then searches TMDB for every show again",
		path:       tvShowCachePath,
	}
	movieSibling = siblingCache{
		name: "mkv-movie's cache",
		lost: "mkv-movie then asks for every rating again",
		path: movieRatingCachePath,
	}
)

var errNoAppData = errors.New("APPDATA not set")

// saveTVShowCacheEntry adds or updates an entry in mkv-tvshow's show_cache.json.
// absPath should be the absolute path to the show folder (the key format mkv-tvshow uses).
func saveTVShowCacheEntry(absPath, tmdbID string) error {
	path := tvShowCachePath()
	if path == "" {
		return errNoAppData
	}
	m, err := readSiblingJSON(path, tvShowSibling.trailingOK)
	if err != nil {
		return err
	}
	if m[absPath], err = json.Marshal(tmdbID); err != nil {
		return err
	}
	return writeSiblingJSON(path, m)
}

// saveMovieTMDBIDEntry adds or updates the tmdb_ids entry in mkv-movie's
// movie_rating_cache.json, keyed "title:YEAR" (mkv-movie's format), and maps
// each of the movie's .mkv files to it. Without a files entry, mkv-movie
// --clean prunes the ID. A file mkv-movie already maps keeps its ID, since
// mkv-movie --clean would otherwise drop the old ID's rating and Plex state.
func saveMovieTMDBIDEntry(title, year, tmdbID string, files []string) error {
	path := movieRatingCachePath()
	if path == "" {
		return errNoAppData
	}
	raw, ids, byFile, err := readMovieCache(path)
	if err != nil {
		return err
	}
	ids[title+":"+year] = tmdbID
	for _, f := range files {
		if !isMKV(f) { // mkv-movie only walks .mkv files
			continue
		}
		abs, err := filepath.Abs(f)
		if err != nil {
			return err
		}
		if _, ok := byFile[abs]; !ok {
			byFile[abs] = tmdbID
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

// readMovieCache reads mkv-movie's cache and the two sections mkv-audit uses.
func readMovieCache(path string) (raw map[string]json.RawMessage, ids, byFile map[string]string, err error) {
	if raw, err = readSiblingJSON(path, movieSibling.trailingOK); err != nil {
		return nil, nil, nil, err
	}
	if ids, err = sectionOf(raw, "tmdb_ids", path); err != nil {
		return nil, nil, nil, err
	}
	if byFile, err = sectionOf(raw, "files", path); err != nil {
		return nil, nil, nil, err
	}
	return raw, ids, byFile, nil
}

// readSiblingJSON reads a sibling tool's JSON object, keeping every key as it
// is. A missing file is empty; one that can't be read or parsed is an error,
// so it is never overwritten (the owning tool refuses to load it, and the
// user can still restore it).
func readSiblingJSON(path string, trailingOK bool) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if trailingOK {
		err = json.NewDecoder(bytes.NewReader(b)).Decode(&raw)
	} else {
		err = json.Unmarshal(b, &raw)
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if raw == nil { // the file holds null
		raw = map[string]json.RawMessage{}
	}
	return raw, nil
}

// sectionOf decodes raw[key] as a string map, empty when absent.
func sectionOf(raw map[string]json.RawMessage, key, path string) (map[string]string, error) {
	m := map[string]string{}
	if b, ok := raw[key]; ok && string(b) != "null" {
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("parse %s: %s: %w", path, key, err)
		}
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
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

// tvShowCachePath returns the path to mkv-tvshow's show_cache.json.
// Format: map[folderName]tmdbID (string values; numeric IDs are also handled).
func tvShowCachePath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "mkv-tvshow", "show_cache.json")
}

// movieRatingCachePath returns the path to mkv-movie's rating_cache.json.
// The relevant section is tmdb_ids: map["title:YEAR"]tmdbID.
func movieRatingCachePath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "mkv-movie", "movie_rating_cache.json")
}

// loadTVShowCache reads mkv-tvshow's show_cache.json. A missing file (or no
// APPDATA) is an empty cache; one that can't be read or parsed is an error.
func loadTVShowCache() (map[string]string, error) {
	path := tvShowCachePath()
	if path == "" {
		return nil, nil
	}
	raw, err := readSiblingJSON(path, tvShowSibling.trailingOK)
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

// loadMovieTMDBIDs reads the tmdb_ids ("title:YEAR" → ID) and files (absolute
// path → ID) sections of mkv-movie's cache. A missing file (or no APPDATA) is
// an empty cache; one that can't be read or parsed is an error.
func loadMovieTMDBIDs() (ids, byFile map[string]string, err error) {
	path := movieRatingCachePath()
	if path == "" {
		return nil, nil, nil
	}
	_, ids, byFile, err = readMovieCache(path)
	return ids, byFile, err
}

// siblingTMDBCaches holds pre-loaded sibling cache data.
type siblingTMDBCaches struct {
	tvShowIDs  map[string]string // folderName → tmdbID
	movieIDs   map[string]string // "title:YEAR" → tmdbID
	movieFiles map[string]string // absolute file path → tmdbID
	// tvErr and movieErr are set once a cache is known to be unloadable or
	// unwritable, so later confirmations skip saving to it.
	tvErr, movieErr error
}

// loadSiblingCaches reads both sibling caches once, warning about each one
// that can't be loaded: the run goes on, but nothing is saved to that cache.
func loadSiblingCaches() *siblingTMDBCaches {
	sc := &siblingTMDBCaches{}
	sc.tvShowIDs, sc.tvErr = loadTVShowCache()
	if sc.tvErr != nil {
		warnSiblingLoad(tvShowSibling, sc.tvErr)
	}
	sc.movieIDs, sc.movieFiles, sc.movieErr = loadMovieTMDBIDs()
	if sc.movieErr != nil {
		warnSiblingLoad(movieSibling, sc.movieErr)
	}
	return sc
}

func warnSiblingLoad(c siblingCache, err error) {
	hint := "Close any program that has it open, then run mkv-audit again."
	if isCorrupt(err) {
		hint = fmt.Sprintf("It is corrupt and was left as it is: restore it from a backup if you have one, or delete it to start over (%s).", c.lost)
	}
	fmt.Printf("[WARN] %s can't be loaded, so TMDB IDs confirmed this run won't be saved to it: %v\n       %s\n", c.name, err, hint)
}

// saveTV saves a confirmed show ID to mkv-tvshow's cache, unless it is known
// to be bad.
func (sc *siblingTMDBCaches) saveTV(dir, absPath, id string) {
	if sc.tvErr == nil {
		if err := saveTVShowCacheEntry(absPath, id); err != nil {
			sc.tvErr = err
			warnSiblingSave(tvShowSibling, dir, id, err)
		}
	}
}

// saveMovie saves a confirmed movie ID to mkv-movie's cache, unless it is
// known to be bad.
func (sc *siblingTMDBCaches) saveMovie(dir, title, year, id string, files []string) {
	if sc.movieErr == nil {
		if err := saveMovieTMDBIDEntry(title, year, id, files); err != nil {
			sc.movieErr = err
			warnSiblingSave(movieSibling, dir, id, err)
		}
	}
}

func warnSiblingSave(c siblingCache, dir, id string, err error) {
	fmt.Printf("[WARN] %s: could not save TMDB id %s to %s: %v\n"+
		"       This run still uses it, but saves nothing more to %s; the next run asks again.\n"+
		"       Close any program that has it open.\n", dir, id, c.name, err, c.name)
}

// lookupTV returns the TMDB show ID for the given folder name, or "".
// It checks both the bare folder name (legacy format) and the absolute path
// (current mkv-tvshow format: filepath.Join(libraryRoot, folderName)).
func (sc *siblingTMDBCaches) lookupTV(folderName, libraryRoot string) string {
	if id := sc.tvShowIDs[folderName]; id != "" {
		return id
	}
	if libraryRoot != "" {
		if id := sc.tvShowIDs[filepath.Join(libraryRoot, folderName)]; id != "" {
			return id
		}
	}
	return ""
}

// lookupMovie returns the TMDB movie ID for the given title+year, or "".
// The key format matches mkv-movie: "title:YEAR" (or "title:" when year is
// empty). mkv-movie builds its titles differently, so failing that, the ID
// mkv-movie recorded for any of the movie's files is used.
func (sc *siblingTMDBCaches) lookupMovie(title, year string, files []string) string {
	if id := sc.movieIDs[title+":"+year]; id != "" {
		return id
	}
	for _, f := range files {
		if abs, err := filepath.Abs(f); err == nil && sc.movieFiles[abs] != "" {
			return sc.movieFiles[abs]
		}
	}
	return ""
}
