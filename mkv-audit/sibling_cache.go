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
	m := loadTVShowCache()
	if m == nil {
		m = map[string]string{}
	}
	m[absPath] = tmdbID
	b, err := json.MarshalIndent(m, "", "    ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

// saveMovieTMDBIDEntry adds or updates the tmdb_ids entry in mkv-movie's
// movie_rating_cache.json. The key is "title:YEAR" (mkv-movie's format).
func saveMovieTMDBIDEntry(title, year, tmdbID string) error {
	path := movieRatingCachePath()
	if path == "" {
		return fmt.Errorf("APPDATA not set")
	}
	// Read existing file, preserving unknown keys.
	var raw map[string]json.RawMessage
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &raw) //nolint:errcheck
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	var ids map[string]string
	if existing, ok := raw["tmdb_ids"]; ok {
		json.Unmarshal(existing, &ids) //nolint:errcheck
	}
	if ids == nil {
		ids = map[string]string{}
	}
	ids[title+":"+year] = tmdbID
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	raw["tmdb_ids"] = json.RawMessage(encoded)
	out, err := json.MarshalIndent(raw, "", "    ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
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
