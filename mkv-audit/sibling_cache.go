package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

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
	return filepath.Join(appData, "mkv-movie", "rating_cache.json")
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
	var raw map[string]interface{}
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
func (sc siblingTMDBCaches) lookupTV(folderName string) string {
	return sc.tvShowIDs[folderName]
}

// lookupMovie returns the TMDB movie ID for the given title+year, or "".
// The key format matches mkv-movie: "title:YEAR" (or "title:" when year is empty).
func (sc siblingTMDBCaches) lookupMovie(title, year string) string {
	key := title + ":" + year
	return sc.movieIDs[key]
}
