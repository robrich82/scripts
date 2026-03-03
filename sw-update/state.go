package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// AppState tracks the known versions of an app.
type AppState struct {
	LatestVersion    string    `json:"latest_version,omitempty"`
	InstalledVersion string    `json:"installed_version,omitempty"`
	LastSeenVersion  string    `json:"last_seen_version,omitempty"`
	LastChecked      time.Time `json:"last_checked,omitempty"`
}

// KnownVersion returns the best locally known version:
// installed_version > last_seen_version > ""
func (as *AppState) KnownVersion() string {
	if as.InstalledVersion != "" {
		return as.InstalledVersion
	}
	return as.LastSeenVersion
}

// State maps app names to their AppState.
type State map[string]*AppState

// Get returns the AppState for the given name, creating it if absent.
func (s State) Get(name string) *AppState {
	if s[name] == nil {
		s[name] = &AppState{}
	}
	return s[name]
}

// LoadState loads the state file from disk.
func LoadState() (State, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "sw-update-state.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return s, nil
}

// SaveState writes the state file to disk.
func SaveState(s State) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "sw-update-state.json")
	data, err := json.MarshalIndent(s, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
