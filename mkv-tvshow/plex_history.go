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

// plexAccount is a Plex server account: id 1 is the owner, the rest are
// plex.tv users with access to the server.
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
	ParentIndex      int    `json:"parentIndex,omitempty"`
	Index            int    `json:"index,omitempty"`
	ViewedAt         int64  `json:"viewedAt"`
	AccountID        int    `json:"accountID"`
}

const historyPageSize = 500

const historyKeyPrefix = "/status/sessions/history/"

// historyID returns the numeric id in a history key, or false for anything
// else: the key comes from the server, and a DELETE goes to it.
func historyID(historyKey string) (string, bool) {
	id, ok := strings.CutPrefix(historyKey, historyKeyPrefix)
	if _, err := strconv.ParseUint(id, 10, 64); !ok || err != nil {
		return "", false
	}
	return id, true
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

// WatchHistory implements HistoryClient, a page at a time. It stops at a
// short page, or once it has totalSize entries, if the server sent that.
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
		added := 0
		for _, e := range page {
			if _, ok := historyID(e.HistoryKey); !ok {
				return nil, fmt.Errorf("plex: watch history entry %q has no valid historyKey (%q)", e.Title, e.HistoryKey)
			}
			// A play logged while paging shifts later pages by one.
			if !seen[e.HistoryKey] {
				seen[e.HistoryKey] = true
				all = append(all, e)
				added++
			}
		}
		start += len(page)
		if len(page) < historyPageSize || added == 0 || (total > 0 && start >= total) {
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
	id, ok := historyID(historyKey)
	if !ok {
		return fmt.Errorf("plex: %q is not a watch history key", historyKey)
	}
	resp, err := c.plexRequest(http.MethodDelete, historyKeyPrefix+id)
	if err != nil {
		return fmt.Errorf("plex: delete watch history: %w", err)
	}
	resp.Body.Close()
	return nil
}
