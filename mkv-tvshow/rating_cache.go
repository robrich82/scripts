package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// RatingCacheData is the JSON-serialisable body of rating_cache.json.
type RatingCacheData struct {
	// Shows maps TMDB show ID → series-level NZRating (from TMDB or Classification Office).
	Shows map[string]string `json:"shows"`
	// Episodes maps "tmdbID/S01E03" → episode-level NZRating (from CO or manual prompt).
	Episodes map[string]string `json:"episodes"`
}

// RatingCache persists NZ ratings across runs so slow scrapes and user-entered
// ratings are not repeated unnecessarily.
type RatingCache struct {
	path string
	data RatingCacheData
}

// LoadRatingCache reads rating_cache.json from path.
// Returns an empty cache (no error) if the file does not exist.
func LoadRatingCache(path string) (*RatingCache, error) {
	c := &RatingCache{
		path: path,
		data: RatingCacheData{
			Shows:    make(map[string]string),
			Episodes: make(map[string]string),
		},
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &c.data); err != nil {
		return nil, err
	}
	if c.data.Shows == nil {
		c.data.Shows = make(map[string]string)
	}
	if c.data.Episodes == nil {
		c.data.Episodes = make(map[string]string)
	}
	return c, nil
}

// Save writes the cache to disk as JSON.
func (c *RatingCache) Save() error {
	data, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, data, 0644)
}

// GetShowRating returns the cached series-level rating for the given TMDB show ID.
func (c *RatingCache) GetShowRating(tmdbID string) (NZRating, bool) {
	s, ok := c.data.Shows[tmdbID]
	if !ok || s == "" {
		return RatingNone, false
	}
	return NZRating(s), true
}

// SetShowRating stores a series-level rating for the given TMDB show ID.
func (c *RatingCache) SetShowRating(tmdbID string, r NZRating) {
	if r != RatingNone {
		c.data.Shows[tmdbID] = string(r)
	}
}

// GetEpisodeRating returns the cached episode-level rating.
func (c *RatingCache) GetEpisodeRating(tmdbID string, season, episode int) (NZRating, bool) {
	s, ok := c.data.Episodes[episodeRatingKey(tmdbID, season, episode)]
	if !ok || s == "" {
		return RatingNone, false
	}
	return NZRating(s), true
}

// SetEpisodeRating stores an episode-level rating.
func (c *RatingCache) SetEpisodeRating(tmdbID string, season, episode int, r NZRating) {
	if r != RatingNone {
		c.data.Episodes[episodeRatingKey(tmdbID, season, episode)] = string(r)
	}
}

// episodeRatingKey returns the map key for an episode, e.g. "12345/S01E03".
func episodeRatingKey(tmdbID string, season, episode int) string {
	return fmt.Sprintf("%s/S%02dE%02d", tmdbID, season, episode)
}
