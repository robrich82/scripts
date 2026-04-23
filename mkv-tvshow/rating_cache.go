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
	// Applied maps absolute file path → rating string already written as Matroska tags.
	// Used by --ratings-only mode to avoid re-writing tags on every run.
	Applied map[string]string `json:"applied,omitempty"`
	// LanguageApplied maps TMDB show ID → BCP-47 language code already set as the
	// preferred audio language in Plex for that show. Used to apply the setting only
	// once so the user can override it per-show inside Plex.
	LanguageApplied map[string]string `json:"language_applied,omitempty"`
	// PlexKeys maps TMDB show ID → Plex ratingKey. Populated during normal runs so
	// --language-only can find shows without scanning files.
	PlexKeys map[string]string `json:"plex_keys,omitempty"`
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
			Shows:           make(map[string]string),
			Episodes:        make(map[string]string),
			Applied:         make(map[string]string),
			LanguageApplied: make(map[string]string),
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
	if c.data.Applied == nil {
		c.data.Applied = make(map[string]string)
	}
	if c.data.LanguageApplied == nil {
		c.data.LanguageApplied = make(map[string]string)
	}
	return c, nil
}

// IsApplied returns true if filePath already has rating r written as Matroska tags.
func (c *RatingCache) IsApplied(filePath string, r NZRating) bool {
	return NZRating(c.data.Applied[filePath]) == r && r != RatingNone
}

// MarkApplied records that filePath has had its rating tags successfully written.
func (c *RatingCache) MarkApplied(filePath string, r NZRating) {
	c.data.Applied[filePath] = string(r)
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

// GetLanguageApplied returns the BCP-47 language code previously applied to
// the Plex show for the given TMDB ID, and whether any value was recorded.
func (c *RatingCache) GetLanguageApplied(tmdbID string) (string, bool) {
	lang, ok := c.data.LanguageApplied[tmdbID]
	return lang, ok && lang != ""
}

// SetLanguageApplied records that the given BCP-47 language code has been set
// as the preferred audio language in Plex for the show identified by tmdbID.
func (c *RatingCache) SetLanguageApplied(tmdbID, lang string) {
	if lang != "" {
		c.data.LanguageApplied[tmdbID] = lang
	}
}

// GetPlexKey returns the cached Plex ratingKey for the given TMDB show ID.
func (c *RatingCache) GetPlexKey(tmdbID string) (string, bool) {
	if c.data.PlexKeys == nil {
		return "", false
	}
	key, ok := c.data.PlexKeys[tmdbID]
	return key, ok && key != ""
}

// SetPlexKey records the Plex ratingKey for the given TMDB show ID.
func (c *RatingCache) SetPlexKey(tmdbID, key string) {
	if key == "" {
		return
	}
	if c.data.PlexKeys == nil {
		c.data.PlexKeys = make(map[string]string)
	}
	c.data.PlexKeys[tmdbID] = key
}

// AllShowIDs returns all TMDB show IDs present in the cache.
func (c *RatingCache) AllShowIDs() []string {
	ids := make([]string, 0, len(c.data.Shows))
	for id := range c.data.Shows {
		ids = append(ids, id)
	}
	return ids
}
