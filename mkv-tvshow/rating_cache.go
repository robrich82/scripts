package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
			PlexKeys:        make(map[string]string),
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
	if c.data.PlexKeys == nil {
		c.data.PlexKeys = make(map[string]string)
	}
	return c, nil
}

// IsApplied returns true if filePath already has rating r written as Matroska tags.
func (c *RatingCache) IsApplied(filePath string, r NZRating) bool {
	return NZRating(c.data.Applied[filePath]) == r && r != RatingNone
}

// MarkApplied records that filePath has had its rating tags successfully
// written. RatingNone clears the entry: the tags hold no rating.
func (c *RatingCache) MarkApplied(filePath string, r NZRating) {
	if r == RatingNone {
		delete(c.data.Applied, filePath)
		return
	}
	c.data.Applied[filePath] = string(r)
}

// CleanStalePaths removes Applied entries whose file paths no longer exist on disk.
// Returns the number of entries removed.
func (c *RatingCache) CleanStalePaths() int {
	removed := 0
	for path := range c.data.Applied {
		if pathGone(path) {
			delete(c.data.Applied, path)
			removed++
		}
	}
	return removed
}

// CleanStaleShowIDs removes Shows, Episodes, LanguageApplied, and PlexKeys
// entries whose TMDB show ID stale reports. Returns the number removed.
func (c *RatingCache) CleanStaleShowIDs(stale func(showID string) bool) int {
	removed := 0
	for id := range c.data.Shows {
		if stale(id) {
			delete(c.data.Shows, id)
			removed++
		}
	}
	for key := range c.data.Episodes {
		showID, _, _ := strings.Cut(key, "/")
		if stale(showID) {
			delete(c.data.Episodes, key)
			removed++
		}
	}
	for id := range c.data.LanguageApplied {
		if stale(id) {
			delete(c.data.LanguageApplied, id)
			removed++
		}
	}
	for id := range c.data.PlexKeys {
		if stale(id) {
			delete(c.data.PlexKeys, id)
			removed++
		}
	}
	return removed
}

// ResetForShowID clears the show-level rating and all episode ratings for the
// given TMDB show ID. Returns the number of episode entries removed.
func (c *RatingCache) ResetForShowID(tmdbID string) int {
	delete(c.data.Shows, tmdbID)
	prefix := tmdbID + "/"
	removed := 0
	for key := range c.data.Episodes {
		if strings.HasPrefix(key, prefix) {
			delete(c.data.Episodes, key)
			removed++
		}
	}
	return removed
}

// ResetAppliedForFolder clears Applied entries for files that fall under
// folderPath (based on filepath.Rel). Returns the file paths that were reset.
func (c *RatingCache) ResetAppliedForFolder(folderPath string) []string {
	var paths []string
	for path := range c.data.Applied {
		rel, err := filepath.Rel(folderPath, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			delete(c.data.Applied, path)
			paths = append(paths, path)
		}
	}
	return paths
}

// Save writes the cache to disk as JSON.
func (c *RatingCache) Save() error {
	data, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(c.path, data)
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
	key, ok := c.data.PlexKeys[tmdbID]
	return key, ok && key != ""
}

// SetPlexKey records the Plex ratingKey for the given TMDB show ID.
func (c *RatingCache) SetPlexKey(tmdbID, key string) {
	if key != "" {
		c.data.PlexKeys[tmdbID] = key
	}
}

// ClearPlexKey removes the cached Plex ratingKey for the given TMDB show ID,
// forcing a fresh library lookup on the next run.
func (c *RatingCache) ClearPlexKey(tmdbID string) {
	delete(c.data.PlexKeys, tmdbID)
}
