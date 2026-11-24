package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The plex.tv item types of a show. The server's history spells them in
// lower case.
const (
	cloudTypeEpisode = "EPISODE"
	cloudTypeSeason  = "SEASON"
	cloudTypeShow    = "SHOW"
)

// cloudEntry is one item of the token owner's watch history on plex.tv, which
// the Plex server's history doesn't include.
type cloudEntry struct {
	ID    string `json:"id"`
	Date  string `json:"date"`
	Type  string `json:"type"`  // EPISODE, SEASON, SHOW or MOVIE
	Title string `json:"title"` // the item's own title
	// ShowID, Show and Year are the show's plex.tv id, title and first year:
	// the item's for a SHOW, its parent's for a SEASON and its grandparent's
	// for an EPISODE.
	ShowID  string `json:"showID,omitempty"`
	Show    string `json:"show,omitempty"`
	Year    int    `json:"year,omitempty"`
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"`
}

// cloudPage is one page of a plex.tv watch history. Cursor is where the next
// page starts, when HasNext.
type cloudPage struct {
	Entries []cloudEntry
	HasNext bool
	Cursor  string
}

// CloudHistoryClient reads and deletes the token owner's plex.tv watch history.
type CloudHistoryClient interface {
	// CloudHistoryPage reads the page of user uuid's history that starts at
	// cursor after; "" is the first page.
	CloudHistoryPage(uuid, after string) (cloudPage, error)
	RemoveActivity(id string) error
}

// rateLimitError is plex.tv refusing a request until After has passed; 0
// when it didn't say how long.
type rateLimitError struct{ After time.Duration }

func (e *rateLimitError) Error() string {
	if e.After <= 0 {
		return "plex.tv rate limit, with no wait given"
	}
	return fmt.Sprintf("plex.tv rate limit: retry after %s", e.After)
}

// cloudPageSize is how many history items each plex.tv request asks for.
const cloudPageSize = 100

// The plex.tv web app's queries; the API's introspection is disabled.
const (
	watchHistoryQuery = `query GetWatchHistoryHub($uuid: ID = "", $first: PaginationInt!, $after: String) {
  user(id: $uuid) {
    watchHistory(first: $first, after: $after) {
      nodes { id date metadataItem { id title type year index parent { id title year index } grandparent { id title year } } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	removeActivityMutation = `mutation removeActivity($input: RemoveActivityInput!) {
  removeActivity(input: $input)
}`
)

type graphQLError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code       string  `json:"code"`
		RetryAfter float64 `json:"retryAfter"` // seconds
	} `json:"extensions"`
}

// graphQL posts a query to plex.tv's community API and decodes its data into
// data. A rate limit, by GraphQL error or HTTP 429, comes back as a
// *rateLimitError; another status than 2xx as a *plexTVStatusError, with the
// reply's GraphQL errors.
func (c *plexTVClient) graphQL(operation, query string, variables map[string]any, data any) error {
	body, err := json.Marshal(map[string]any{"operationName": operation, "query": query, "variables": variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.communityURL+"/api", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	setPlexTVHeaders(req, c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex.tv: %s: %w", operation, err)
	}
	defer resp.Body.Close()
	var reply struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&reply)
	if limited := rateLimitIn(reply.Errors); limited != nil {
		return limited
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &rateLimitError{After: retryAfterHeader(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode/100 != 2 {
		statusErr := &plexTVStatusError{what: operation, Status: resp.Status, Code: resp.StatusCode}
		if len(reply.Errors) > 0 {
			return fmt.Errorf("%w: %s", statusErr, errorMessages(reply.Errors))
		}
		return statusErr
	}
	if decodeErr != nil {
		return fmt.Errorf("plex.tv: decode %s: %w", operation, decodeErr)
	}
	if len(reply.Errors) > 0 {
		return fmt.Errorf("plex.tv: %s: %s", operation, errorMessages(reply.Errors))
	}
	if len(reply.Data) == 0 || string(reply.Data) == "null" {
		return fmt.Errorf("plex.tv: %s: no data in the reply", operation)
	}
	if err := json.Unmarshal(reply.Data, data); err != nil {
		return fmt.Errorf("plex.tv: decode %s: %w", operation, err)
	}
	return nil
}

// rateLimitIn is the rate limit among errs, or nil.
func rateLimitIn(errs []graphQLError) *rateLimitError {
	for _, e := range errs {
		if e.Extensions.Code == "RATE_LIMITED" {
			secs := min(max(e.Extensions.RetryAfter, 0), 24*60*60) // a Duration overflows near 292 years
			return &rateLimitError{After: time.Duration(secs * float64(time.Second))}
		}
	}
	return nil
}

func errorMessages(errs []graphQLError) string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, cmp.Or(e.Message, e.Extensions.Code, "an unnamed error"))
	}
	return strings.Join(msgs, "; ")
}

// retryAfterHeader reads a Retry-After of whole seconds; 0 when absent or a date.
func retryAfterHeader(v string) time.Duration {
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// cloudRef is an item, or its parent or grandparent, in a history node.
type cloudRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Year  int    `json:"year"`
	Index int    `json:"index"`
}

type cloudItem struct {
	cloudRef
	Type        string   `json:"type"`
	Parent      cloudRef `json:"parent"`
	Grandparent cloudRef `json:"grandparent"`
}

// cloudEntryOf flattens a history node, naming the show the item belongs to.
func cloudEntryOf(id, date string, item cloudItem) cloudEntry {
	e := cloudEntry{ID: id, Date: date, Type: item.Type, Title: item.Title}
	var show cloudRef
	switch item.Type {
	case cloudTypeShow:
		show = item.cloudRef
	case cloudTypeSeason:
		e.Season = item.Index
		show = item.Parent
	case cloudTypeEpisode:
		e.Season, e.Episode = item.Parent.Index, item.Index
		show = item.Grandparent
	}
	e.ShowID, e.Show, e.Year = show.ID, show.Title, show.Year
	return e
}

// CloudHistoryPage implements CloudHistoryClient.
func (c *plexTVClient) CloudHistoryPage(uuid, after string) (cloudPage, error) {
	if uuid == "" {
		return cloudPage{}, errors.New("plex.tv: no user id to read the watch history of")
	}
	var data struct {
		User *struct {
			WatchHistory *struct {
				Nodes []struct {
					ID           string    `json:"id"`
					Date         string    `json:"date"`
					MetadataItem cloudItem `json:"metadataItem"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"watchHistory"`
		} `json:"user"`
	}
	vars := map[string]any{"uuid": uuid, "first": cloudPageSize, "after": nil}
	if after != "" {
		vars["after"] = after
	}
	if err := c.graphQL("GetWatchHistoryHub", watchHistoryQuery, vars, &data); err != nil {
		return cloudPage{}, err
	}
	if data.User == nil || data.User.WatchHistory == nil {
		return cloudPage{}, errors.New("plex.tv: GetWatchHistoryHub: no watch history in the reply")
	}
	wh := data.User.WatchHistory
	page := cloudPage{HasNext: wh.PageInfo.HasNextPage, Cursor: wh.PageInfo.EndCursor}
	for _, n := range wh.Nodes {
		if n.ID == "" {
			return cloudPage{}, errors.New("plex.tv: a watch history item has no id")
		}
		page.Entries = append(page.Entries, cloudEntryOf(n.ID, n.Date, n.MetadataItem))
	}
	return page, nil
}

// RemoveActivity implements CloudHistoryClient: it deletes one watch history
// item. Only a reply of true counts. A rate limit comes back as a
// *rateLimitError for the caller to wait out.
func (c *plexTVClient) RemoveActivity(id string) error {
	if id == "" {
		return errors.New("plex.tv: no watch history item id to remove")
	}
	var data struct {
		RemoveActivity *bool `json:"removeActivity"`
	}
	vars := map[string]any{"input": map[string]any{"id": id, "type": "WATCH_HISTORY"}}
	if err := c.graphQL("removeActivity", removeActivityMutation, vars, &data); err != nil {
		return err
	}
	switch {
	case data.RemoveActivity == nil:
		return errors.New("plex.tv: removeActivity: the reply doesn't say it was removed")
	case !*data.RemoveActivity:
		return errors.New("plex.tv: removeActivity: plex.tv didn't remove it")
	}
	return nil
}
