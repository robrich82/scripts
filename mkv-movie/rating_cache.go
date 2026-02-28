package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	Movies  map[string]string `json:"movies"`
	Applied map[string]string `json:"applied,omitempty"` // absFilePath → rating already written
}

// LoadMovieRatingCache loads the cache from path. If the file does not exist an
// empty cache is returned. Any other read/parse error is returned as an error.
func LoadMovieRatingCache(path string) (*MovieRatingCache, error) {
	c := &MovieRatingCache{
		path: path,
		data: movieRatingCacheData{
			Movies:  make(map[string]string),
			Applied: make(map[string]string),
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
	return c, nil
}

// IsApplied returns true if the file at filePath already has rating r written as tags.
func (c *MovieRatingCache) IsApplied(filePath string, r NZRating) bool {
	return NZRating(c.data.Applied[filePath]) == r && r != RatingNone
}

// MarkApplied records that the given file has had its rating tags successfully written.
func (c *MovieRatingCache) MarkApplied(filePath string, r NZRating) {
	c.data.Applied[filePath] = string(r)
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
