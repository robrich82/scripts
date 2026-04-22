package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrArtistNotFound is returned by FindArtistByTitle when no matching artist
// exists in any Plex music library section.
var ErrArtistNotFound = errors.New("plex: artist not found")

// PlexClient is the interface for the Plex Media Server calls needed by
// mkv-music: finding a music artist and triggering a metadata refresh.
type PlexClient interface {
	// FindArtistByTitle searches all "artist" (music) library sections for an
	// item whose normalised title matches title. Returns the ratingKey on
	// success, or ErrArtistNotFound if no match exists.
	FindArtistByTitle(title string) (string, error)
	// RefreshMetadata triggers a metadata refresh for the item identified by
	// ratingKey, causing Plex to re-read embedded metadata from the files.
	RefreshMetadata(ratingKey string) error
}

// httpPlexClient is the production PlexClient that communicates with a real
// Plex Media Server over HTTP.
type httpPlexClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newHTTPPlexClient(baseURL, token string) *httpPlexClient {
	return &httpPlexClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{},
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

// FindArtistByTitle implements PlexClient. It scans all "artist" (music)
// library sections for an item whose normalised title matches title.
func (c *httpPlexClient) FindArtistByTitle(title string) (string, error) {
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

	normTitle := normalizeMusicTitle(title)
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "artist" {
			continue
		}
		key, err := c.findArtistInSection(section.Key, normTitle)
		if err != nil {
			return "", err
		}
		if key != "" {
			return key, nil
		}
	}
	return "", ErrArtistNotFound
}

func (c *httpPlexClient) findArtistInSection(sectionKey, normTitle string) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=8")
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
		if normalizeMusicTitle(item.Title) == normTitle {
			return item.RatingKey, nil
		}
	}
	return "", nil
}

// RefreshMetadata implements PlexClient.
func (c *httpPlexClient) RefreshMetadata(ratingKey string) error {
	resp, err := c.plexGet("/library/metadata/" + ratingKey + "/refresh")
	if err != nil {
		return fmt.Errorf("plex: refresh metadata %s: %w", ratingKey, err)
	}
	resp.Body.Close()
	return nil
}

// normalizeMusicTitle returns a lowercase alphanumeric-only string for fuzzy
// title matching against Plex artist titles.
func normalizeMusicTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}
