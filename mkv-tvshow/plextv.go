package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// plex.tv's account API and its community API, which holds the watch history.
// Only tests change them.
var (
	plexTVBaseURL      = "https://plex.tv"
	plexTVCommunityURL = "https://community.plex.tv"
)

// plexTVClient calls plex.tv as the PLEX_TOKEN owner.
type plexTVClient struct {
	baseURL      string
	communityURL string
	token        string
	httpClient   *http.Client
}

func newPlexTVClient(baseURL, communityURL, token string) *plexTVClient {
	httpClient := newPlexHTTPClient()
	// plex.tv's API doesn't redirect. Following one would send a removal on
	// without its token, where a reply of true proves nothing, or turn a lost
	// token into a 401 that reads as plex.tv refusing it.
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return fmt.Errorf("plex.tv redirected %s %s to %s; refusing to follow it", via[0].Method, via[0].URL.Path, req.URL.Redacted())
	}
	return &plexTVClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		communityURL: strings.TrimRight(communityURL, "/"),
		token:        token,
		httpClient:   httpClient,
	}
}

// setPlexTVHeaders authenticates req to plex.tv. The token goes in a header,
// so transport errors can't print it, and plex.tv refuses requests without a
// client identifier.
func setPlexTVHeaders(req *http.Request, token string) {
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("X-Plex-Client-Identifier", "mkv-tvshow")
	req.Header.Set("X-Plex-Product", "mkv-tvshow")
}

// plexTVUser is the plex.tv account a token belongs to.
type plexTVUser struct {
	Username string `json:"username"`
	UUID     string `json:"uuid"`
}

// plexTVStatusError is plex.tv answering with a status other than 2xx.
type plexTVStatusError struct {
	what   string // the request, as "get user"
	Status string
	Code   int
}

func (e *plexTVStatusError) Error() string {
	return fmt.Sprintf("plex.tv: %s: unexpected status %s", e.what, e.Status)
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
	setPlexTVHeaders(req, c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return plexTVUser{}, fmt.Errorf("plex.tv: get user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return plexTVUser{}, &plexTVStatusError{what: "get user", Status: resp.Status, Code: resp.StatusCode}
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
