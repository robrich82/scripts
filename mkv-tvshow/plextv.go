package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// plexTVBaseURL is plex.tv's account API.
const plexTVBaseURL = "https://plex.tv"

// plexTVClient calls plex.tv as the PLEX_TOKEN owner.
type plexTVClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newPlexTVClient(baseURL, token string) *plexTVClient {
	return &plexTVClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: newPlexHTTPClient(),
	}
}

// plexTVUser is the plex.tv account a token belongs to.
type plexTVUser struct {
	Username string `json:"username"`
	UUID     string `json:"uuid"`
}

// OwnerLookup finds the account that owns the Plex token.
type OwnerLookup interface {
	User() (plexTVUser, error)
}

// User implements OwnerLookup.
func (c *plexTVClient) User() (plexTVUser, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/api/v2/user", nil)
	if err != nil {
		return plexTVUser{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token) // a header, so transport errors can't print it
	// plex.tv refuses requests without a client identifier.
	req.Header.Set("X-Plex-Client-Identifier", "mkv-tvshow")
	req.Header.Set("X-Plex-Product", "mkv-tvshow")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return plexTVUser{}, fmt.Errorf("plex.tv: get user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return plexTVUser{}, fmt.Errorf("plex.tv: get user: unexpected status %s", resp.Status)
	}
	var u plexTVUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return plexTVUser{}, fmt.Errorf("plex.tv: decode user: %w", err)
	}
	if u.Username == "" {
		return plexTVUser{}, errors.New("plex.tv: get user: no username in the response")
	}
	return u, nil
}
