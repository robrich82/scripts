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

// cloudEntry is one item of the token owner's watch history on plex.tv, which
// the Plex server's history doesn't include.
type cloudEntry struct {
	ID    string `json:"id"`
	Date  string `json:"date"`
	Type  string `json:"type"`  // EPISODE, SEASON, SHOW or MOVIE
	Title string `json:"title"` // the item's own title
	// Show and Year are the show's title and first year: the item's for a
	// SHOW, its parent's for a SEASON and its grandparent's for an EPISODE.
	Show    string `json:"show,omitempty"`
	Year    int    `json:"year,omitempty"`
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"`
}

// CloudHistoryClient reads and deletes the token owner's plex.tv watch history.
type CloudHistoryClient interface {
	CloudHistory(uuid string) ([]cloudEntry, error)
	RemoveActivity(id string) error
}

// rateLimitError is plex.tv refusing a request until After has passed.
type rateLimitError struct{ After time.Duration }

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("plex.tv rate limit: retry after %s", e.After)
}

// cloudPageSize is how many history items each plex.tv request asks for.
const cloudPageSize = 100

// The plex.tv web app's queries; the API's introspection is disabled.
const (
	watchHistoryQuery = `query GetWatchHistoryHub($uuid: ID = "", $first: PaginationInt!, $after: String) {
  user(id: $uuid) {
    watchHistory(first: $first, after: $after) {
      nodes { id date metadataItem { id title type year index parent { title year index } grandparent { title year } } }
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
// data. A rate-limit error comes back as a *rateLimitError.
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
	if resp.StatusCode == http.StatusTooManyRequests {
		return &rateLimitError{After: retryAfterHeader(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("plex.tv: %s: unexpected status %s", operation, resp.Status)
	}
	var reply struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return fmt.Errorf("plex.tv: decode %s: %w", operation, err)
	}
	if len(reply.Errors) > 0 {
		var msgs []string
		for _, e := range reply.Errors {
			if e.Extensions.Code == "RATE_LIMITED" {
				return &rateLimitError{After: time.Duration(e.Extensions.RetryAfter * float64(time.Second))}
			}
			msgs = append(msgs, cmp.Or(e.Message, e.Extensions.Code, "an unnamed error"))
		}
		return fmt.Errorf("plex.tv: %s: %s", operation, strings.Join(msgs, "; "))
	}
	if len(reply.Data) == 0 || string(reply.Data) == "null" {
		return fmt.Errorf("plex.tv: %s: no data in the reply", operation)
	}
	if err := json.Unmarshal(reply.Data, data); err != nil {
		return fmt.Errorf("plex.tv: decode %s: %w", operation, err)
	}
	return nil
}

// retryAfterHeader reads a Retry-After of whole seconds; 0 when absent or a date.
func retryAfterHeader(v string) time.Duration {
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

type cloudItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Type   string `json:"type"`
	Year   int    `json:"year"`
	Index  int    `json:"index"`
	Parent *struct {
		Title string `json:"title"`
		Year  int    `json:"year"`
		Index int    `json:"index"`
	} `json:"parent"`
	Grandparent *struct {
		Title string `json:"title"`
		Year  int    `json:"year"`
	} `json:"grandparent"`
}

// cloudEntryOf flattens a history node, naming the show the item belongs to.
func cloudEntryOf(id, date string, item cloudItem) cloudEntry {
	e := cloudEntry{ID: id, Date: date, Type: item.Type, Title: item.Title}
	switch item.Type {
	case "SHOW":
		e.Show, e.Year = item.Title, item.Year
	case "SEASON":
		e.Season = item.Index
		if item.Parent != nil {
			e.Show, e.Year = item.Parent.Title, item.Parent.Year
		}
	case "EPISODE":
		e.Episode = item.Index
		if item.Parent != nil {
			e.Season = item.Parent.Index
		}
		if item.Grandparent != nil {
			e.Show, e.Year = item.Grandparent.Title, item.Grandparent.Year
		}
	}
	return e
}

// CloudHistory implements CloudHistoryClient: every item in the watch history
// of the plex.tv user uuid, newest first.
func (c *plexTVClient) CloudHistory(uuid string) ([]cloudEntry, error) {
	if uuid == "" {
		return nil, errors.New("plex.tv: no user id to read the watch history of")
	}
	var entries []cloudEntry
	seenCursor := make(map[string]bool)
	seenID := make(map[string]bool)
	var after any // null for the first page
	for {
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
		vars := map[string]any{"uuid": uuid, "first": cloudPageSize, "after": after}
		if err := c.graphQL("GetWatchHistoryHub", watchHistoryQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.User == nil || data.User.WatchHistory == nil {
			return nil, errors.New("plex.tv: GetWatchHistoryHub: no watch history in the reply")
		}
		wh := data.User.WatchHistory
		for _, n := range wh.Nodes {
			if n.ID == "" {
				return nil, errors.New("plex.tv: a watch history item has no id")
			}
			if !seenID[n.ID] {
				seenID[n.ID] = true
				entries = append(entries, cloudEntryOf(n.ID, n.Date, n.MetadataItem))
			}
		}
		if !wh.PageInfo.HasNextPage {
			return entries, nil
		}
		cursor := wh.PageInfo.EndCursor
		if cursor == "" || seenCursor[cursor] {
			return nil, errors.New("plex.tv: the watch history's next page repeats or has no cursor, so it can't be read whole")
		}
		seenCursor[cursor] = true
		after = cursor
	}
}

// RemoveActivity implements CloudHistoryClient: it deletes one watch history
// item. A rate limit comes back as a *rateLimitError for the caller to wait out.
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
	if data.RemoveActivity != nil && !*data.RemoveActivity {
		return errors.New("plex.tv: removeActivity: plex.tv didn't remove it")
	}
	return nil
}
