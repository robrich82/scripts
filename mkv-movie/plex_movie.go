package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrPlexMovieNotFound is returned by FindMovieByTMDBID when no matching movie
// exists in any Plex library section.
var ErrPlexMovieNotFound = errors.New("plex: movie not found")

// PlexMovieClient is the interface for Plex API calls needed by the movie
// rating pipeline.
type PlexMovieClient interface {
	// FindMovieByTMDBID searches all "movie" library sections for an item whose
	// Guid contains "tmdb://{tmdbID}". Returns the ratingKey on success, or
	// ErrPlexMovieNotFound if no match exists.
	FindMovieByTMDBID(tmdbID string) (string, error)
	// FindMovieByTitle searches all "movie" library sections for an item whose
	// title (normalized) matches title and whose year matches year.
	// Returns ErrPlexMovieNotFound if no match exists.
	FindMovieByTitle(title, year string) (string, error)
	// GetContentRating returns the current NZ content rating stored in Plex for
	// the item identified by ratingKey, or RatingNone if unset or unrecognised.
	GetContentRating(ratingKey string) (NZRating, error)
	// SetContentRating updates the contentRating field on a Plex movie item and
	// locks it to prevent automatic overwriting.
	SetContentRating(ratingKey string, rating NZRating) error
	// RefreshLibraries triggers a metadata scan of all Plex movie library
	// sections so that newly processed files are picked up without manual
	// intervention.
	RefreshLibraries() error
}

// httpPlexMovieClient is the production PlexMovieClient that communicates with
// a real Plex Media Server over HTTP.
type httpPlexMovieClient struct {
	baseURL       string
	token         string
	httpClient    *http.Client
	movieKeyCache map[string]string // tmdbID → Plex ratingKey
}

func newHTTPPlexMovieClient(baseURL, token string) *httpPlexMovieClient {
	return &httpPlexMovieClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		token:         token,
		httpClient:    &http.Client{},
		movieKeyCache: make(map[string]string),
	}
}

func (c *httpPlexMovieClient) plexGet(path string) (*http.Response, error) {
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

// FindMovieByTMDBID implements PlexMovieClient.
func (c *httpPlexMovieClient) FindMovieByTMDBID(tmdbID string) (string, error) {
	if key, ok := c.movieKeyCache[tmdbID]; ok {
		return key, nil
	}

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

	wantGUID := "tmdb://" + tmdbID
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "movie" {
			continue
		}
		key, err := c.findMovieInSection(section.Key, wantGUID, tmdbID)
		if err != nil {
			return "", err
		}
		if key != "" {
			c.movieKeyCache[tmdbID] = key
			return key, nil
		}
	}
	return "", ErrPlexMovieNotFound
}

func (c *httpPlexMovieClient) findMovieInSection(sectionKey, wantGUID, tmdbID string) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=1")
	if err != nil {
		return "", fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string          `json:"ratingKey"`
				Guid      json.RawMessage `json:"Guid"`
				GuidLower string          `json:"guid"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	for _, item := range allResp.MediaContainer.Metadata {
		if plexMovieItemHasTMDBID(item.Guid, item.GuidLower, wantGUID, tmdbID) {
			return item.RatingKey, nil
		}
	}
	return "", nil
}

// plexMovieItemHasTMDBID mirrors mkv-tvshow's plexItemHasTMDBID.
func plexMovieItemHasTMDBID(guidRaw json.RawMessage, guidLower, wantGUID, tmdbID string) bool {
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
	if len(guidRaw) > 0 {
		var s string
		if json.Unmarshal(guidRaw, &s) == nil && plexMovieGuidMatchesTMDBID(s, tmdbID) {
			return true
		}
	}
	return plexMovieGuidMatchesTMDBID(guidLower, tmdbID)
}

func plexMovieGuidMatchesTMDBID(s, tmdbID string) bool {
	target := "://" + tmdbID
	idx := strings.Index(s, target)
	if idx == -1 {
		return false
	}
	rest := s[idx+len(target):]
	return rest == "" || rest[0] < '0' || rest[0] > '9'
}

// FindMovieByTitle implements PlexMovieClient.
func (c *httpPlexMovieClient) FindMovieByTitle(title, year string) (string, error) {
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

	normTitle := normalizePlexTitle(title)
	wantYear := 0
	if year != "" {
		wantYear, _ = strconv.Atoi(year)
	}
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "movie" {
			continue
		}
		key, err := c.findMovieInSectionByTitle(section.Key, normTitle, wantYear)
		if err != nil {
			return "", err
		}
		if key != "" {
			return key, nil
		}
	}
	return "", ErrPlexMovieNotFound
}

func (c *httpPlexMovieClient) findMovieInSectionByTitle(sectionKey, normTitle string, wantYear int) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=1")
	if err != nil {
		return "", fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
				Year      int    `json:"year"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	for _, item := range allResp.MediaContainer.Metadata {
		if normalizePlexTitle(item.Title) != normTitle {
			continue
		}
		if wantYear != 0 && item.Year != wantYear {
			continue
		}
		return item.RatingKey, nil
	}
	return "", nil
}

// normalizePlexTitle returns a lowercase alphanumeric-only string for fuzzy title matching.
func normalizePlexTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// GetContentRating implements PlexMovieClient.
// It returns the NZ content rating currently stored in Plex for ratingKey,
// stripping the "nz/" prefix before parsing. Returns RatingNone when unset
// or not a recognised NZ rating.
func (c *httpPlexMovieClient) GetContentRating(ratingKey string) (NZRating, error) {
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

// RefreshLibraries implements PlexMovieClient. It scans all "movie" library
// sections, triggering a metadata refresh so Plex picks up any new or changed
// files without requiring a manual library scan.
func (c *httpPlexMovieClient) RefreshLibraries() error {
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
		if section.Type != "movie" {
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

// SetContentRating implements PlexMovieClient.
func (c *httpPlexMovieClient) SetContentRating(ratingKey string, rating NZRating) error {
	plexRating := "nz/" + string(rating)
	path := fmt.Sprintf("/library/metadata/%s?type=1&id=%s&contentRating.value=%s&contentRating.locked=1&X-Plex-Token=%s",
		ratingKey, ratingKey, url.QueryEscape(plexRating), url.QueryEscape(c.token))

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
