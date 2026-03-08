package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrPlexNotFound is returned by FindShowByTMDBID when no matching show exists
// in any Plex library section.
var ErrPlexNotFound = errors.New("plex: show not found")

// PlexItemType is the numeric type identifier used in Plex API calls.
type PlexItemType int

const (
	PlexTypeShow    PlexItemType = 2
	PlexTypeSeason  PlexItemType = 3
	PlexTypeEpisode PlexItemType = 4
)

// PlexSeason holds the Plex ratingKey and season number for a season item.
type PlexSeason struct {
	RatingKey    string
	SeasonNumber int
}

// PlexEpisode holds the Plex ratingKey, season number, and episode number.
type PlexEpisodeItem struct {
	RatingKey     string
	SeasonNumber  int
	EpisodeNumber int
}

// PlexClient is the interface for all Plex Media Server API calls needed by
// the rating pipeline.
type PlexClient interface {
	// FindShowByTMDBID searches all "show" library sections for an item whose
	// Guid list contains "tmdb://{tmdbID}". Returns the ratingKey on success,
	// or ErrPlexNotFound if no match exists.
	FindShowByTMDBID(tmdbID string) (string, error)
	// FindShowByTitle searches all "show" library sections for an item whose
	// title (normalised) matches title. Used as a fallback when the show uses a
	// non-TMDB agent (e.g. HAMA for anime). Returns ErrPlexNotFound if absent.
	FindShowByTitle(title string) (string, error)
	// FindShowByFilePath searches all "show" library sections for the show that
	// owns the episode file at filePath. It queries episode metadata filtered by
	// part.file and returns the grandparentRatingKey (show ratingKey) of the
	// matching episode. Used as a last resort when TMDB GUID and title matching
	// both fail (e.g. when the Plex title differs from the filesystem folder
	// name). Returns ErrPlexNotFound if no episode with that path exists.
	FindShowByFilePath(filePath string) (string, error)
	// GetSeasons returns the ratingKey and season number for every season of
	// the show identified by showRatingKey.
	GetSeasons(showRatingKey string) ([]PlexSeason, error)
	// GetEpisodes returns the ratingKey and episode/season numbers for every
	// episode of the season identified by seasonRatingKey.
	GetEpisodes(seasonRatingKey string) ([]PlexEpisodeItem, error)
	// GetContentRating returns the NZ content rating currently stored in Plex
	// for the item identified by ratingKey, stripping the "nz/" prefix before
	// parsing. Returns RatingNone when unset or not a recognised NZ rating.
	GetContentRating(ratingKey string) (NZRating, error)
	// SetContentRating updates the contentRating field on a Plex item and
	// locks it to prevent automatic overwriting.
	SetContentRating(ratingKey string, itemType PlexItemType, rating NZRating) error
	// RefreshLibraries triggers a metadata scan of all Plex TV show library
	// sections so that newly processed files are picked up without manual
	// intervention.
	RefreshLibraries() error
}

// httpPlexClient is the production PlexClient that communicates with a real
// Plex Media Server over HTTP.
type httpPlexClient struct {
	baseURL    string
	token      string
	httpClient *http.Client

	// showKeyCache caches tmdbID → Plex ratingKey within a single run to avoid
	// repeatedly scanning the full library.
	showKeyCache map[string]string
}

func newHTTPPlexClient(baseURL, token string) *httpPlexClient {
	return &httpPlexClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		token:        token,
		httpClient:   &http.Client{},
		showKeyCache: make(map[string]string),
	}
}

// plexGet performs a GET request to path (relative to baseURL), appending the
// Plex token and an Accept: application/json header.
func (c *httpPlexClient) plexGet(path string) (*http.Response, error) {
	u := c.baseURL + path
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	u += sep + "X-Plex-Token=" + url.QueryEscape(c.token)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return c.httpClient.Do(req)
}

// FindShowByTMDBID implements PlexClient.
func (c *httpPlexClient) FindShowByTMDBID(tmdbID string) (string, error) {
	if key, ok := c.showKeyCache[tmdbID]; ok {
		return key, nil
	}

	// 1. Discover all library sections.
	resp, err := c.plexGet("/library/sections")
	if err != nil {
		return "", fmt.Errorf("plex: list sections: %w", err)
	}
	defer resp.Body.Close()

	var sectionsResp struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return "", fmt.Errorf("plex: decode sections: %w", err)
	}

	// 2. For each "show" section, scan all shows for the TMDB GUID.
	wantGUID := "tmdb://" + tmdbID
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "show" {
			continue
		}
		key, err := c.findInSection(section.Key, wantGUID)
		if err != nil {
			return "", err
		}
		if key != "" {
			c.showKeyCache[tmdbID] = key
			return key, nil
		}
	}
	return "", ErrPlexNotFound
}

// findInSection scans a single library section for a show with the given GUID.
func (c *httpPlexClient) findInSection(sectionKey, wantGUID string) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=2")
	if err != nil {
		return "", fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	// Guid may be a JSON array (modern Plex TV agent) or a plain string
	// (legacy metadata agents). Use RawMessage so we can try both.
	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string          `json:"ratingKey"`
				Guid      json.RawMessage `json:"Guid"`
				GuidLower string          `json:"guid"` // legacy lowercase plain-string guid
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	// tmdbID is the bare numeric ID extracted from wantGUID ("tmdb://12345" → "12345").
	tmdbID := strings.TrimPrefix(wantGUID, "tmdb://")

	for _, item := range allResp.MediaContainer.Metadata {
		if plexItemHasTMDBID(item.Guid, item.GuidLower, wantGUID, tmdbID) {
			return item.RatingKey, nil
		}
	}
	return "", nil
}

// plexItemHasTMDBID reports whether a Plex metadata item matches the given
// TMDB ID. It handles three GUID representations:
//
//  1. Modern (Plex TV agent): Guid is a JSON array, e.g. [{"id":"tmdb://12345"}]
//  2. Legacy Guid string:     Guid is a plain JSON string containing "://tmdbID"
//  3. Legacy lowercase guid:  guid field is a plain string, e.g.
//     "com.plexapp.agents.themoviedb://12345?lang=en"
func plexItemHasTMDBID(guidRaw json.RawMessage, guidLower, wantGUID, tmdbID string) bool {
	// Modern format: array of objects.
	if len(guidRaw) > 0 && guidRaw[0] == '[' {
		var guids []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(guidRaw, &guids) == nil {
			for _, g := range guids {
				if g.ID == wantGUID {
					return true
				}
			}
		}
		return false
	}

	// Legacy: Guid is a plain JSON string.
	if len(guidRaw) > 0 {
		var s string
		if json.Unmarshal(guidRaw, &s) == nil && guidStringMatchesTMDBID(s, tmdbID) {
			return true
		}
	}

	// Legacy: lowercase guid field.
	return guidStringMatchesTMDBID(guidLower, tmdbID)
}

// guidStringMatchesTMDBID returns true when s contains "://tmdbID" followed
// by a non-digit (or end of string), preventing partial-ID false positives.
func guidStringMatchesTMDBID(s, tmdbID string) bool {
	target := "://" + tmdbID
	idx := strings.Index(s, target)
	if idx == -1 {
		return false
	}
	rest := s[idx+len(target):]
	return rest == "" || rest[0] < '0' || rest[0] > '9'
}

// GetSeasons implements PlexClient.
func (c *httpPlexClient) GetSeasons(showRatingKey string) ([]PlexSeason, error) {
	resp, err := c.plexGet("/library/metadata/" + showRatingKey + "/children")
	if err != nil {
		return nil, fmt.Errorf("plex: get seasons for %s: %w", showRatingKey, err)
	}
	defer resp.Body.Close()

	var childResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Type      string `json:"type"`
				Index     int    `json:"index"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&childResp); err != nil {
		return nil, fmt.Errorf("plex: decode seasons for %s: %w", showRatingKey, err)
	}

	var seasons []PlexSeason
	for _, m := range childResp.MediaContainer.Metadata {
		if m.Type == "season" {
			seasons = append(seasons, PlexSeason{RatingKey: m.RatingKey, SeasonNumber: m.Index})
		}
	}
	return seasons, nil
}

// GetEpisodes implements PlexClient.
func (c *httpPlexClient) GetEpisodes(seasonRatingKey string) ([]PlexEpisodeItem, error) {
	resp, err := c.plexGet("/library/metadata/" + seasonRatingKey + "/children")
	if err != nil {
		return nil, fmt.Errorf("plex: get episodes for %s: %w", seasonRatingKey, err)
	}
	defer resp.Body.Close()

	var childResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey   string `json:"ratingKey"`
				Type        string `json:"type"`
				ParentIndex int    `json:"parentIndex"`
				Index       int    `json:"index"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&childResp); err != nil {
		return nil, fmt.Errorf("plex: decode episodes for %s: %w", seasonRatingKey, err)
	}

	var episodes []PlexEpisodeItem
	for _, m := range childResp.MediaContainer.Metadata {
		if m.Type == "episode" {
			episodes = append(episodes, PlexEpisodeItem{
				RatingKey:     m.RatingKey,
				SeasonNumber:  m.ParentIndex,
				EpisodeNumber: m.Index,
			})
		}
	}
	return episodes, nil
}

// FindShowByTitle implements PlexClient. It scans all "show" library sections
// for an item whose normalised title matches title, regardless of which
// metadata agent is in use. This is a fallback for when FindShowByTMDBID fails
// because the show uses a non-TMDB agent (e.g. HAMA for anime).
func (c *httpPlexClient) FindShowByTitle(title string) (string, error) {
	resp, err := c.plexGet("/library/sections")
	if err != nil {
		return "", fmt.Errorf("plex: list sections: %w", err)
	}
	defer resp.Body.Close()

	var sectionsResp struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return "", fmt.Errorf("plex: decode sections: %w", err)
	}

	normTitle := normalizeShowTitle(title)
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "show" {
			continue
		}
		key, err := c.findShowInSectionByTitle(section.Key, normTitle)
		if err != nil {
			return "", err
		}
		if key != "" {
			return key, nil
		}
	}
	return "", ErrPlexNotFound
}

func (c *httpPlexClient) findShowInSectionByTitle(sectionKey, normTitle string) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=2")
	if err != nil {
		return "", fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	for _, item := range allResp.MediaContainer.Metadata {
		if normalizeShowTitle(item.Title) == normTitle {
			return item.RatingKey, nil
		}
	}
	return "", nil
}

// FindShowByFilePath implements PlexClient. It queries each show library
// section for episodes (type=4) whose Part file matches filePath exactly,
// then returns the grandparentRatingKey of the first match — the show's Plex
// ratingKey. This is the most reliable fallback for shows whose Plex title
// differs from the filesystem folder name (e.g. anime using HAMA agent).
func (c *httpPlexClient) FindShowByFilePath(filePath string) (string, error) {
	resp, err := c.plexGet("/library/sections")
	if err != nil {
		return "", fmt.Errorf("plex: list sections: %w", err)
	}
	defer resp.Body.Close()

	var sectionsResp struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return "", fmt.Errorf("plex: decode sections: %w", err)
	}

	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "show" {
			continue
		}
		key, err := c.findShowInSectionByFilePath(section.Key, filePath)
		if err != nil {
			return "", err
		}
		if key != "" {
			return key, nil
		}
	}
	return "", ErrPlexNotFound
}

func (c *httpPlexClient) findShowInSectionByFilePath(sectionKey, filePath string) (string, error) {
	path := "/library/sections/" + sectionKey + "/all?type=4&part.file=" + url.QueryEscape(filePath)
	resp, err := c.plexGet(path)
	if err != nil {
		return "", fmt.Errorf("plex: list section %s by file: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				GrandparentRatingKey string `json:"grandparentRatingKey"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s file results: %w", sectionKey, err)
	}

	if len(allResp.MediaContainer.Metadata) > 0 {
		return allResp.MediaContainer.Metadata[0].GrandparentRatingKey, nil
	}
	return "", nil
}

// normalizeShowTitle returns a lowercase alphanumeric-only string for fuzzy
// title matching, consistent with normalizePlexTitle in mkv-movie.
func normalizeShowTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// GetContentRating implements PlexClient.
func (c *httpPlexClient) GetContentRating(ratingKey string) (NZRating, error) {
	resp, err := c.plexGet("/library/metadata/" + ratingKey)
	if err != nil {
		return RatingNone, fmt.Errorf("plex: get metadata %s: %w", ratingKey, err)
	}
	defer resp.Body.Close()

	var metaResp struct {
		MediaContainer struct {
			Metadata []struct {
				ContentRating string `json:"contentRating"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&metaResp); err != nil {
		return RatingNone, fmt.Errorf("plex: decode metadata %s: %w", ratingKey, err)
	}
	if len(metaResp.MediaContainer.Metadata) == 0 {
		return RatingNone, nil
	}
	raw := strings.TrimPrefix(metaResp.MediaContainer.Metadata[0].ContentRating, "nz/")
	rating, _ := ParseNZRating(raw)
	return rating, nil
}

// RefreshLibraries implements PlexClient. It scans all "show" library
// sections, triggering a metadata refresh so Plex picks up any new or changed
// files without requiring a manual library scan.
func (c *httpPlexClient) RefreshLibraries() error {
	resp, err := c.plexGet("/library/sections")
	if err != nil {
		return fmt.Errorf("plex: list sections: %w", err)
	}
	defer resp.Body.Close()

	var sectionsResp struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return fmt.Errorf("plex: decode sections: %w", err)
	}

	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "show" {
			continue
		}
		refreshResp, err := c.plexGet("/library/sections/" + section.Key + "/refresh")
		if err != nil {
			return fmt.Errorf("plex: refresh section %s: %w", section.Key, err)
		}
		refreshResp.Body.Close()
	}
	return nil
}

// SetContentRating implements PlexClient. It sends a PUT to the metadata
// endpoint setting contentRating and locking the field.
func (c *httpPlexClient) SetContentRating(ratingKey string, itemType PlexItemType, rating NZRating) error {
	// Plex identifies content rating systems by a country prefix (e.g. "nz/M",
	// "nz/R16"). Without the prefix the value is stored as a raw string and
	// won't be recognised as an NZ classification rating.
	plexRating := "nz/" + string(rating)
	path := fmt.Sprintf("/library/metadata/%s?type=%d&id=%s&contentRating.value=%s&contentRating.locked=1&X-Plex-Token=%s",
		ratingKey, itemType, ratingKey, url.QueryEscape(plexRating), url.QueryEscape(c.token))

	req, err := http.NewRequest(http.MethodPut, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("plex: build put request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex: set content rating: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("plex: set content rating got status %d", resp.StatusCode)
	}
	return nil
}
