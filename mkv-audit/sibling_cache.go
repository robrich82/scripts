package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// saveTVShowCacheEntry adds or updates an entry in mkv-tvshow's show_cache.json.
// absPath should be the absolute path to the show folder (the key format mkv-tvshow uses).
func saveTVShowCacheEntry(absPath, tmdbID string) error {
	path := tvShowCachePath()
	if path == "" {
		return fmt.Errorf("APPDATA not set")
	}
	m, err := readSiblingJSON(path)
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
// each of the movie's files to it. Without a files entry, mkv-movie --clean
// prunes the ID.
func saveMovieTMDBIDEntry(title, year, tmdbID string, files []string) error {
	path := movieRatingCachePath()
	if path == "" {
		return fmt.Errorf("APPDATA not set")
	}
	raw, err := readSiblingJSON(path)
	if err != nil {
		return err
	}
	ids, err := sectionOf(raw, "tmdb_ids", path)
	if err != nil {
		return err
	}
	byFile, err := sectionOf(raw, "files", path)
	if err != nil {
		return err
	}
	ids[title+":"+year] = tmdbID
	for _, f := range files {
		if abs, err := filepath.Abs(f); err == nil {
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

// readSiblingJSON reads a sibling tool's JSON object, keeping every key as it
// is. A missing file is empty; one that can't be read or parsed is an error,
// so it is never overwritten (the owning tool refuses to load it, and the
// user can still restore it).
func readSiblingJSON(path string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s is corrupt, so it was left alone: %w", path, err)
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
			return nil, fmt.Errorf("%s: %s is corrupt, so the file was left alone: %w", path, key, err)
		}
	}
	return m, nil
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

// loadTVShowCache reads mkv-tvshow's show_cache.json and returns the map.
// Returns nil on any error (missing file, parse failure, etc.).
func loadTVShowCache() map[string]string {
	path := tvShowCachePath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// The Python-generated cache may store IDs as numbers; decode via interface{}.
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil
	}
	m := make(map[string]string, len(raw))
	for k, v := range raw {
		switch val := v.(type) {
		case string:
			m[k] = val
		case float64:
			m[k] = fmt.Sprintf("%d", int(val))
		}
	}
	return m
}

// loadMovieTMDBIDs reads the tmdb_ids section of mkv-movie's rating_cache.json.
// Keys are "title:YEAR"; returns nil on any error.
func loadMovieTMDBIDs() map[string]string {
	path := movieRatingCachePath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var data struct {
		TMDBIDs map[string]string `json:"tmdb_ids"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	return data.TMDBIDs
}

// siblingTMDBCaches holds pre-loaded sibling cache data.
type siblingTMDBCaches struct {
	tvShowIDs map[string]string // folderName → tmdbID
	movieIDs  map[string]string // "title:YEAR" → tmdbID
}

// loadSiblingCaches reads both sibling caches once. Either map may be nil if
// the corresponding cache file is absent or unreadable.
func loadSiblingCaches() siblingTMDBCaches {
	return siblingTMDBCaches{
		tvShowIDs: loadTVShowCache(),
		movieIDs:  loadMovieTMDBIDs(),
	}
}

// lookupTV returns the TMDB show ID for the given folder name, or "".
// It checks both the bare folder name (legacy format) and the absolute path
// (current mkv-tvshow format: filepath.Join(libraryRoot, folderName)).
func (sc siblingTMDBCaches) lookupTV(folderName, libraryRoot string) string {
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
// The key format matches mkv-movie: "title:YEAR" (or "title:" when year is empty).
func (sc siblingTMDBCaches) lookupMovie(title, year string) string {
	key := title + ":" + year
	return sc.movieIDs[key]
}
