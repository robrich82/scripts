package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// httpTimeout bounds every outbound Plex request so a hung connection can't
// freeze a whole run indefinitely.
const httpTimeout = 30 * time.Second

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
	// AnalyzeMedia triggers a media analysis for the item and all its children,
	// causing Plex to re-read codec, resolution, and other stream properties.
	// This is needed when a file's codec changes (e.g. AV1 → HEVC) so Plex
	// stops transcoding using the old codec profile.
	AnalyzeMedia(ratingKey string) error
}

// httpPlexClient is the production PlexClient that communicates with a real
// Plex Media Server over HTTP.
type httpPlexClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// newPlexHTTPClient drops X-Plex-Token from a redirect that leaves the Plex
// server's host: Go only strips Authorization and Cookie on its own.
func newPlexHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("X-Plex-Token")
			}
			return nil
		},
	}
}

func newHTTPPlexClient(baseURL, token string) *httpPlexClient {
	return &httpPlexClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: newPlexHTTPClient(),
	}
}

// plexGet performs a GET request to path (relative to baseURL) with an
// Accept: application/json header. The Plex token goes in a header, not the
// URL, so transport errors (which print the URL) can't leak it.
func (c *httpPlexClient) plexGet(path string) (*http.Response, error) {
	u := c.baseURL + path
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)
	return doPlexRequest(c.httpClient, req, path)
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

// plexPut performs a PUT request to path (relative to baseURL), sending the
// Plex token in a header.
func (c *httpPlexClient) plexPut(path string) (*http.Response, error) {
	u := c.baseURL + path
	req, err := http.NewRequest(http.MethodPut, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)
	return doPlexRequest(c.httpClient, req, path)
}

// doPlexRequest issues req and rejects non-2xx responses with an error, so a
// 401/404/500 from Plex surfaces as a failure instead of being decoded as a
// zero-value "success" (which would make FindArtistByTitle report "not found"
// and RefreshMetadata/AnalyzeMedia report success on a server error).
func doPlexRequest(client *http.Client, req *http.Request, path string) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("plex: %s %s: unexpected status %s", req.Method, path, resp.Status)
	}
	return resp, nil
}

// RefreshMetadata implements PlexClient. It sends a PUT, the only method the
// Plex API defines for the item refresh endpoint.
func (c *httpPlexClient) RefreshMetadata(ratingKey string) error {
	resp, err := c.plexPut("/library/metadata/" + ratingKey + "/refresh")
	if err != nil {
		return fmt.Errorf("plex: refresh metadata %s: %w", ratingKey, err)
	}
	resp.Body.Close()
	return nil
}

// AnalyzeMedia implements PlexClient. It sends a PUT to the analyze endpoint,
// causing Plex to re-read codec and stream properties for the item and all
// its children (albums and tracks under an artist).
func (c *httpPlexClient) AnalyzeMedia(ratingKey string) error {
	resp, err := c.plexPut("/library/metadata/" + ratingKey + "/analyze")
	if err != nil {
		return fmt.Errorf("plex: analyze media %s: %w", ratingKey, err)
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
