package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// MovieRatingCache persists NZ content ratings for movies across runs so that
// external sources and user prompts are not repeated unnecessarily.
// The on-disk format is: {"movies":{"tmdbID":"M"}}
// Keys are TMDB IDs when available, or "title:YEAR" as a fallback.
type MovieRatingCache struct {
	path string
	data movieRatingCacheData
}

type movieRatingCacheData struct {
	Movies         map[string]string `json:"movies"`
	Applied        map[string]string `json:"applied,omitempty"`         // absFilePath → rating already written
	TMDBIDs        map[string]string `json:"tmdb_ids,omitempty"`        // "title:YEAR" → tmdbID
	MetaApplied    map[string]string `json:"meta_applied,omitempty"`    // absFilePath → tmdbID (full metadata written)
	PlexRatings    map[string]string `json:"plex_ratings,omitempty"`    // tmdbID → rating last pushed to Plex
	StreamsApplied map[string]string `json:"streams_applied,omitempty"` // tmdbID → audio lang tag when streams were last set
}

// LoadMovieRatingCache loads the cache from path. If the file does not exist an
// empty cache is returned. Any other read/parse error is returned as an error.
func LoadMovieRatingCache(path string) (*MovieRatingCache, error) {
	c := &MovieRatingCache{
		path: path,
		data: movieRatingCacheData{
			Movies:         make(map[string]string),
			Applied:        make(map[string]string),
			TMDBIDs:        make(map[string]string),
			MetaApplied:    make(map[string]string),
			PlexRatings:    make(map[string]string),
			StreamsApplied: make(map[string]string),
		},
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rating cache: read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &c.data); err != nil {
		return nil, fmt.Errorf("rating cache: parse %s: %w", path, err)
	}
	if c.data.Movies == nil {
		c.data.Movies = make(map[string]string)
	}
	if c.data.Applied == nil {
		c.data.Applied = make(map[string]string)
	}
	if c.data.TMDBIDs == nil {
		c.data.TMDBIDs = make(map[string]string)
	}
	if c.data.MetaApplied == nil {
		c.data.MetaApplied = make(map[string]string)
	}
	if c.data.PlexRatings == nil {
		c.data.PlexRatings = make(map[string]string)
	}
	if c.data.StreamsApplied == nil {
		c.data.StreamsApplied = make(map[string]string)
	}
	return c, nil
}

// Get returns the cached rating for key (TMDB ID or "title:YEAR").
func (c *MovieRatingCache) Get(key string) (NZRating, bool) {
	s, ok := c.data.Movies[key]
	if !ok || s == "" {
		return RatingNone, false
	}
	return NZRating(s), true
}

// Set stores a rating for key.
func (c *MovieRatingCache) Set(key string, r NZRating) {
	c.data.Movies[key] = string(r)
}

// GetTMDBID returns the cached TMDB ID for the given "title:YEAR" key, or "".
func (c *MovieRatingCache) GetTMDBID(key string) string {
	return c.data.TMDBIDs[key]
}

// SetTMDBID stores a TMDB ID for the given "title:YEAR" key.
func (c *MovieRatingCache) SetTMDBID(key, id string) {
	c.data.TMDBIDs[key] = id
}

// IsMetaApplied returns true if the file at filePath already has full metadata
// from the given TMDB ID written to it.
func (c *MovieRatingCache) IsMetaApplied(filePath, tmdbID string) bool {
	return c.data.MetaApplied[filePath] == tmdbID && tmdbID != ""
}

// MarkMetaApplied records that filePath has had its full metadata written from tmdbID.
func (c *MovieRatingCache) MarkMetaApplied(filePath, tmdbID string) {
	c.data.MetaApplied[filePath] = tmdbID
}

// GetPlexRating returns the rating that was last successfully pushed to Plex
// for the given key (TMDB ID), or ("", false) if never pushed.
func (c *MovieRatingCache) GetPlexRating(key string) (NZRating, bool) {
	s, ok := c.data.PlexRatings[key]
	if !ok || s == "" {
		return RatingNone, false
	}
	return NZRating(s), true
}

// SetPlexRating records the rating that was successfully pushed to Plex for key.
func (c *MovieRatingCache) SetPlexRating(key string, r NZRating) {
	c.data.PlexRatings[key] = string(r)
}

// GetStreamsApplied returns the audio language tag that was used when Plex
// streams were last selected for the given TMDB ID, and whether any value was recorded.
func (c *MovieRatingCache) GetStreamsApplied(tmdbID string) (string, bool) {
	lang, ok := c.data.StreamsApplied[tmdbID]
	return lang, ok && lang != ""
}

// SetStreamsApplied records that Plex streams were selected for the movie
// identified by tmdbID using the given audio language tag.
func (c *MovieRatingCache) SetStreamsApplied(tmdbID, lang string) {
	if lang != "" {
		c.data.StreamsApplied[tmdbID] = lang
	}
}

// CleanStalePaths removes Applied and MetaApplied entries whose file paths no
// longer exist on disk. Returns the counts of entries removed from each map.
func (c *MovieRatingCache) CleanStalePaths() (applied, metaApplied int) {
	for path := range c.data.Applied {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			delete(c.data.Applied, path)
			applied++
		}
	}
	for path := range c.data.MetaApplied {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			delete(c.data.MetaApplied, path)
			metaApplied++
		}
	}
	return
}

// FindTMDBIDByQuery returns all (tmdbID, displayKey) pairs matching query.
// Query may be a direct TMDB ID present in Movies, or a case-insensitive substring
// of a "title (YEAR)" key in TMDBIDs. Falls back to substring matching Movies keys.
func (c *MovieRatingCache) FindTMDBIDByQuery(query string) [][2]string {
	var matches [][2]string
	seen := make(map[string]bool)
	lq := strings.ToLower(query)

	// Direct key hit in Movies (e.g. bare TMDB ID or legacy "title:YEAR").
	if _, ok := c.data.Movies[query]; ok && !seen[query] {
		matches = append(matches, [2]string{query, query})
		seen[query] = true
	}

	// Substring match in TMDBIDs ("title (YEAR)" → tmdbID).
	for titleKey, id := range c.data.TMDBIDs {
		if strings.Contains(strings.ToLower(titleKey), lq) && !seen[id] {
			matches = append(matches, [2]string{id, titleKey})
			seen[id] = true
		}
	}

	// Fall back to substring match on Movies keys (legacy "title:YEAR" or bare IDs).
	if len(matches) == 0 {
		for key := range c.data.Movies {
			if strings.Contains(strings.ToLower(key), lq) && !seen[key] {
				matches = append(matches, [2]string{key, key})
				seen[key] = true
			}
		}
	}
	return matches
}

// ResetForTMDBID clears the cached rating, Plex rating, and all applied-state
// entries for files identified by the given TMDB ID. Returns the reset file paths.
func (c *MovieRatingCache) ResetForTMDBID(tmdbID string) []string {
	delete(c.data.Movies, tmdbID)
	delete(c.data.PlexRatings, tmdbID)
	var paths []string
	for path, id := range c.data.MetaApplied {
		if id == tmdbID {
			delete(c.data.Applied, path)
			delete(c.data.MetaApplied, path)
			paths = append(paths, path)
		}
	}
	return paths
}

// Save writes the cache to disk.
func (c *MovieRatingCache) Save() error {
	b, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return fmt.Errorf("rating cache: marshal: %w", err)
	}
	if err := os.WriteFile(c.path, b, 0644); err != nil {
		return fmt.Errorf("rating cache: write %s: %w", c.path, err)
	}
	return nil
}
