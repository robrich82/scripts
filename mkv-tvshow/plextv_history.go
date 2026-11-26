package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

// unconfirmedError is a 2xx reply from plex.tv that doesn't confirm what it
// did: one cut off, one that isn't just the JSON asked for, or a removal
// answered anything but true. Re-reading the history afterwards tells whether
// a removal took. cut is a reply cut off, which may mean plex.tv went away.
type unconfirmedError struct {
	msg string
	cut bool
}

func (e *unconfirmedError) Error() string { return e.msg }

// cloudPageSize is how many history items each plex.tv request asks for.
const cloudPageSize = 100

// The watch history query and the removal mutation, named as
// github.com/martadams89/plex-community-watch-history-cleanup sends them.
// plex.tv's introspection is disabled, so neither can be checked against a
// schema, and clearHistory re-reads the history rather than trust the
// mutation's reply.
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
// reply's GraphQL errors; a 2xx reply cut off, empty, followed by more, or
// with no errors and no readable data, as an *unconfirmedError.
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
	// The status says what it can even when the body is cut off.
	raw, readErr := io.ReadAll(resp.Body)
	var reply struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	var decodeErr error
	trailing := false // anything after the first JSON value
	if readErr == nil {
		dec := json.NewDecoder(bytes.NewReader(raw))
		decodeErr = dec.Decode(&reply)
		trailing = decodeErr == nil && len(bytes.TrimSpace(raw[dec.InputOffset():])) > 0
	}
	header := retryAfterHeader(resp.Header.Get("Retry-After"))
	if limited := rateLimitIn(reply.Errors); limited != nil {
		limited.After = cmp.Or(limited.After, header)
		return limited
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &rateLimitError{After: header}
	}
	if resp.StatusCode/100 != 2 {
		statusErr := &plexTVStatusError{what: operation, Status: resp.Status, Code: resp.StatusCode}
		switch {
		case len(reply.Errors) > 0:
			return fmt.Errorf("%w: %s", statusErr, errorMessages(reply.Errors))
		case readErr != nil:
			return fmt.Errorf("%w (reading the reply: %v)", statusErr, readErr)
		}
		return statusErr
	}
	// A body ending mid-JSON was cut off too, when nothing said how long it was.
	if readErr == nil && errors.Is(decodeErr, io.ErrUnexpectedEOF) {
		readErr = decodeErr
	}
	switch {
	case readErr != nil:
		return &unconfirmedError{msg: fmt.Sprintf("plex.tv: %s: the reply was cut off: %v", operation, readErr), cut: true}
	case errors.Is(decodeErr, io.EOF):
		return &unconfirmedError{msg: fmt.Sprintf("plex.tv: %s: the reply was empty", operation)}
	case decodeErr != nil:
		return &unconfirmedError{msg: fmt.Sprintf("plex.tv: decode %s: %v", operation, decodeErr)}
	case len(reply.Errors) > 0:
		return fmt.Errorf("plex.tv: %s: %s", operation, errorMessages(reply.Errors))
	case trailing:
		return &unconfirmedError{msg: fmt.Sprintf("plex.tv: %s: the reply has more after its JSON", operation)}
	}
	if len(reply.Data) == 0 || string(reply.Data) == "null" {
		return &unconfirmedError{msg: fmt.Sprintf("plex.tv: %s: no data in the reply", operation)}
	}
	if err := json.Unmarshal(reply.Data, data); err != nil {
		return &unconfirmedError{msg: fmt.Sprintf("plex.tv: decode %s: %v", operation, err)}
	}
	return nil
}

// rateLimitIn is the rate limit among errs, or nil.
func rateLimitIn(errs []graphQLError) *rateLimitError {
	for _, e := range errs {
		if e.Extensions.Code == "RATE_LIMITED" {
			secs := min(max(e.Extensions.RetryAfter, 0), maxRetryAfter)
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

// retryAfterHeader reads a Retry-After of whole seconds, up to a day; 0 when
// absent, negative or a date.
func retryAfterHeader(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(min(secs, maxRetryAfter)) * time.Second
}

// maxRetryAfter is the longest wait, in seconds, a rate limit is read as: a
// Duration overflows near 292 years.
const maxRetryAfter = 24 * 60 * 60

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
// item. A reply other than true is an *unconfirmedError, and a rate limit a
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
		return &unconfirmedError{msg: "plex.tv: removeActivity: the reply doesn't say it was removed"}
	case !*data.RemoveActivity:
		return &unconfirmedError{msg: "plex.tv: removeActivity: plex.tv replied false"}
	}
	return nil
}
