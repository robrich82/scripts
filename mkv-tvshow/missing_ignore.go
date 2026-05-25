package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// MissingIgnoreList persists items that the user chose to ignore during
// missing-episode checks. Items can be individual episodes, whole seasons,
// or whole shows.
type MissingIgnoreList struct {
	path     string
	Shows    map[string]bool // TMDB show ID → ignored
	Seasons  map[string]bool // "showID/S01" → ignored
	Episodes map[string]bool // "showID/S01E02" → ignored
}

// LoadMissingIgnoreList reads the ignore list from path. If the file does not
// exist an empty list is returned without error.
func LoadMissingIgnoreList(path string) (*MissingIgnoreList, error) {
	l := &MissingIgnoreList{
		path:     path,
		Shows:    make(map[string]bool),
		Seasons:  make(map[string]bool),
		Episodes: make(map[string]bool),
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, err
	}
	var raw struct {
		Shows    []string `json:"shows"`
		Seasons  []string `json:"seasons"`
		Episodes []string `json:"episodes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, s := range raw.Shows {
		l.Shows[s] = true
	}
	for _, s := range raw.Seasons {
		l.Seasons[s] = true
	}
	for _, e := range raw.Episodes {
		l.Episodes[e] = true
	}
	return l, nil
}

// AddShow marks an entire show as ignored.
func (l *MissingIgnoreList) AddShow(showID string) {
	l.Shows[showID] = true
}

// AddSeason marks a whole season of a show as ignored.
func (l *MissingIgnoreList) AddSeason(showID string, season int) {
	l.Seasons[fmt.Sprintf("%s/S%02d", showID, season)] = true
}

// AddEpisode marks a single episode as ignored.
func (l *MissingIgnoreList) AddEpisode(showID string, season, episode int) {
	l.Episodes[fmt.Sprintf("%s/S%02dE%02d", showID, season, episode)] = true
}

// IsShowIgnored reports whether the entire show is on the ignore list.
func (l *MissingIgnoreList) IsShowIgnored(showID string) bool {
	return l.Shows[showID]
}

// IsSeasonIgnored reports whether the season (or its whole show) is ignored.
func (l *MissingIgnoreList) IsSeasonIgnored(showID string, season int) bool {
	return l.Shows[showID] || l.Seasons[fmt.Sprintf("%s/S%02d", showID, season)]
}

// IsEpisodeIgnored reports whether the episode (or its season or show) is ignored.
func (l *MissingIgnoreList) IsEpisodeIgnored(showID string, season, episode int) bool {
	return l.Shows[showID] ||
		l.Seasons[fmt.Sprintf("%s/S%02d", showID, season)] ||
		l.Episodes[fmt.Sprintf("%s/S%02dE%02d", showID, season, episode)]
}

// CleanStaleShowIDs removes Shows, Seasons, and Episodes entries whose TMDB
// show ID is not present in knownIDs. Returns the number of entries removed.
func (l *MissingIgnoreList) CleanStaleShowIDs(knownIDs map[string]bool) int {
	removed := 0
	for id := range l.Shows {
		if !knownIDs[id] {
			delete(l.Shows, id)
			removed++
		}
	}
	for key := range l.Seasons {
		showID, _, _ := strings.Cut(key, "/")
		if !knownIDs[showID] {
			delete(l.Seasons, key)
			removed++
		}
	}
	for key := range l.Episodes {
		showID, _, _ := strings.Cut(key, "/")
		if !knownIDs[showID] {
			delete(l.Episodes, key)
			removed++
		}
	}
	return removed
}

// Save writes the ignore list to disk as indented JSON.
func (l *MissingIgnoreList) Save() error {
	shows := make([]string, 0, len(l.Shows))
	for k := range l.Shows {
		shows = append(shows, k)
	}
	sort.Strings(shows)

	seasons := make([]string, 0, len(l.Seasons))
	for k := range l.Seasons {
		seasons = append(seasons, k)
	}
	sort.Strings(seasons)

	episodes := make([]string, 0, len(l.Episodes))
	for k := range l.Episodes {
		episodes = append(episodes, k)
	}
	sort.Strings(episodes)

	raw := struct {
		Shows    []string `json:"shows"`
		Seasons  []string `json:"seasons"`
		Episodes []string `json:"episodes"`
	}{shows, seasons, episodes}

	data, err := json.MarshalIndent(raw, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, data, 0644)
}
