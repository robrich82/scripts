package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	// SetContentRating updates the contentRating field on a Plex movie item and
	// locks it to prevent automatic overwriting.
	SetContentRating(ratingKey string, rating NZRating) error
}

// httpPlexMovieClient is the production PlexMovieClient that communicates with
// a real Plex Media Server over HTTP.
type httpPlexMovieClient struct {
	baseURL      string
	token        string
	httpClient   *http.Client
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
