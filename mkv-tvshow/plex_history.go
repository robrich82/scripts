package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// HistoryClient reads and deletes a Plex server's watch history.
type HistoryClient interface {
	Accounts() ([]plexAccount, error)
	// WatchHistory returns every history entry of the account with this id.
	WatchHistory(accountID int) ([]historyEntry, error)
	DeleteHistory(historyKey string) error
}

// plexAccount is a Plex server account: id 1 is the owner, and the rest
// are plex.tv users with access to the server, apart from any with no name.
type plexAccount struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// historyEntry is one play in a server account's watch history. An entry
// outlives the item it played, so its titles are all that is left of a
// removed show; every field goes into the record of deleted entries.
type historyEntry struct {
	HistoryKey       string `json:"historyKey"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	GrandparentTitle string `json:"grandparentTitle,omitempty"`
	GrandparentKey   string `json:"grandparentKey,omitempty"`
	ParentIndex      int    `json:"parentIndex,omitempty"`
	Index            int    `json:"index,omitempty"`
	ViewedAt         int64  `json:"viewedAt"`
	AccountID        int    `json:"accountID"`
}

const historyPageSize = 500

const historyKeyPrefix = "/status/sessions/history/"

// validHistoryKey reports whether historyKey is a history entry's path and
// nothing else: the key comes from the server, and a DELETE goes to it.
func validHistoryKey(historyKey string) bool {
	id, ok := strings.CutPrefix(historyKey, historyKeyPrefix)
	_, err := strconv.ParseUint(id, 10, 64)
	return ok && err == nil
}

// Accounts implements HistoryClient.
func (c *httpPlexClient) Accounts() ([]plexAccount, error) {
	resp, err := c.plexGet("/accounts")
	if err != nil {
		return nil, fmt.Errorf("plex: list accounts: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		MediaContainer struct {
			Account []plexAccount `json:"Account"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("plex: decode accounts: %w", err)
	}
	return body.MediaContainer.Account, nil
}

// WatchHistory implements HistoryClient, a page at a time. It stops at an
// empty page, or once it has totalSize entries if the server sent that: a
// page can be short of the size asked for with more to come.
func (c *httpPlexClient) WatchHistory(accountID int) ([]historyEntry, error) {
	var all []historyEntry
	seen := make(map[string]bool)
	for start := 0; ; {
		path := fmt.Sprintf("/status/sessions/history/all?accountID=%d&sort=viewedAt:desc&X-Plex-Container-Start=%d&X-Plex-Container-Size=%d",
			accountID, start, historyPageSize)
		page, total, err := c.historyPage(path)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return all, nil
		}
		added := 0
		for _, e := range page {
			if !validHistoryKey(e.HistoryKey) {
				return nil, fmt.Errorf("plex: watch history entry %q has no valid historyKey (%q)", e.Title, e.HistoryKey)
			}
			// A play logged while paging shifts later pages by one.
			if !seen[e.HistoryKey] {
				seen[e.HistoryKey] = true
				all = append(all, e)
				added++
			}
		}
		if added == 0 {
			// Repeats that fill the page, or fall short of totalSize: the
			// server is ignoring the offset.
			if len(page) >= historyPageSize || (total > 0 && start+len(page) < total) {
				return nil, fmt.Errorf("plex: the server sent the same watch history again at offset %d, so it can't be read in full", start)
			}
			return all, nil // the tail, shifted by plays logged while paging
		}
		start += len(page)
		if total > 0 && start >= total {
			return all, nil
		}
	}
}

func (c *httpPlexClient) historyPage(path string) ([]historyEntry, int, error) {
	resp, err := c.plexGet(path)
	if err != nil {
		return nil, 0, fmt.Errorf("plex: list watch history: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		MediaContainer struct {
			TotalSize int            `json:"totalSize"`
			Metadata  []historyEntry `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, 0, fmt.Errorf("plex: decode watch history: %w", err)
	}
	return body.MediaContainer.Metadata, body.MediaContainer.TotalSize, nil
}

// DeleteHistory implements HistoryClient.
func (c *httpPlexClient) DeleteHistory(historyKey string) error {
	if !validHistoryKey(historyKey) {
		return fmt.Errorf("plex: %q is not a watch history key", historyKey)
	}
	resp, err := c.plexRequest(http.MethodDelete, historyKey)
	if err != nil {
		return fmt.Errorf("plex: delete watch history: %w", err)
	}
	resp.Body.Close()
	return nil
}
