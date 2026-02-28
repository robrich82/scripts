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
	// GetSeasons returns the ratingKey and season number for every season of
	// the show identified by showRatingKey.
	GetSeasons(showRatingKey string) ([]PlexSeason, error)
	// GetEpisodes returns the ratingKey and episode/season numbers for every
	// episode of the season identified by seasonRatingKey.
	GetEpisodes(seasonRatingKey string) ([]PlexEpisodeItem, error)
	// SetContentRating updates the contentRating field on a Plex item and
	// locks it to prevent automatic overwriting.
	SetContentRating(ratingKey string, itemType PlexItemType, rating NZRating) error
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

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Guid      []struct {
					ID string `json:"id"`
				} `json:"Guid"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	for _, item := range allResp.MediaContainer.Metadata {
		for _, g := range item.Guid {
			if g.ID == wantGUID {
				return item.RatingKey, nil
			}
		}
	}
	return "", nil
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

// SetContentRating implements PlexClient. It sends a PUT to the metadata
// endpoint setting contentRating and locking the field.
func (c *httpPlexClient) SetContentRating(ratingKey string, itemType PlexItemType, rating NZRating) error {
	path := fmt.Sprintf("/library/metadata/%s?type=%d&id=%s&contentRating.value=%s&contentRating.locked=1&X-Plex-Token=%s",
		ratingKey, itemType, ratingKey, url.QueryEscape(string(rating)), url.QueryEscape(c.token))

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
