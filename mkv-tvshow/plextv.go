package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	httpClient.Transport = refuseRedirects{cmp.Or[http.RoundTripper](httpClient.Transport, http.DefaultTransport)}
	return &plexTVClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		communityURL: strings.TrimRight(communityURL, "/"),
		token:        token,
		httpClient:   httpClient,
	}
}

// refuseRedirects turns every redirect from plex.tv into an error, before
// the client reads its Location. plex.tv's API doesn't redirect. Following
// one would send a request on to wherever it points, where even a reply of
// true to a removal proves nothing, or turn a lost token into a 401 that
// reads as plex.tv refusing it. And the client's own errors would print the
// Location whole, where a query could carry a token.
type refuseRedirects struct{ next http.RoundTripper }

func (t refuseRedirects) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		to := cmp.Or(queryless(loc), "an address with only a query, fragment or user info")
		if loc == "" {
			to = "no address"
		}
		return nil, fmt.Errorf("plex.tv redirected %s %s to %s; refusing to follow it", req.Method, req.URL.Path, to)
	}
	return resp, nil
}

// queryless is a URL without its query, fragment or user info, any of which
// could carry a token; one that can't be parsed isn't shown.
func queryless(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "an address that can't be read"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
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

// tokenRefused is err's plex.tv 401 or 403, which a server's own token gets
// too, or nil.
func tokenRefused(err error) *plexTVStatusError {
	var status *plexTVStatusError
	if errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) {
		return status
	}
	return nil
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
