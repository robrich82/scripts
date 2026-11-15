package main

import (
	"bytes"
	"encoding/json"
	"os"
)

// maxRetryAttempts is how many runs in a row a file may fail a step before
// the retry list gives up on it.
const maxRetryAttempts = 5

// RetryList is a persistent set of file paths to re-process on every run
// regardless of mod-time cutoff: ones without a real air date yet, whose TMDB
// lookup failed, or whose tag write, poster or timestamps failed. Each entry
// counts the runs in a row that failed a step; waiting on an air date or a
// TMDB lookup doesn't count.
type RetryList struct {
	path string
	data map[string]int // path → failed runs in a row
}

// LoadRetryList reads the retry list from path. Returns an empty list without
// error if the file does not exist. Reads both the current object form
// ({"path": attempts}) and the older array of paths.
func LoadRetryList(path string) (*RetryList, error) {
	r := &RetryList{path: path, data: make(map[string]int)}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return r, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		var paths []string
		if err := json.Unmarshal(data, &paths); err != nil {
			return nil, err
		}
		for _, p := range paths {
			r.data[p] = 0
		}
		return r, nil
	}
	var entries map[string]int
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	for p, n := range entries {
		r.data[p] = n
	}
	return r, nil
}

// Has reports whether path is in the retry list.
func (r *RetryList) Has(path string) bool {
	_, ok := r.data[path]
	return ok
}

// Add adds path to the retry list, keeping its failed-run count if it is
// already there.
func (r *RetryList) Add(path string) {
	if _, ok := r.data[path]; !ok {
		r.data[path] = 0
	}
}

// Keep keeps path on the retry list after a run in which no step failed,
// clearing its failed-run count.
func (r *RetryList) Keep(path string) {
	r.data[path] = 0
}

// Fail records a run in which a step failed for path. After maxRetryAttempts
// such runs in a row it removes path and returns true.
func (r *RetryList) Fail(path string) (gaveUp bool) {
	r.data[path]++
	if r.data[path] >= maxRetryAttempts {
		delete(r.data, path)
		return true
	}
	return false
}

// Attempts returns the failed runs in a row recorded for path.
func (r *RetryList) Attempts(path string) int {
	return r.data[path]
}

// Remove removes path from the retry list.
func (r *RetryList) Remove(path string) {
	delete(r.data, path)
}

// Len returns the number of entries in the retry list.
func (r *RetryList) Len() int {
	return len(r.data)
}

// CleanStalePaths removes entries for file paths that no longer exist on disk.
// Returns the number of entries removed.
func (r *RetryList) CleanStalePaths() int {
	removed := 0
	for path := range r.data {
		if pathGone(path) {
			delete(r.data, path)
			removed++
		}
	}
	return removed
}

// Save persists the retry list to disk as a JSON object of path → failed runs.
func (r *RetryList) Save() error {
	data, err := json.MarshalIndent(r.data, "", "    ")
	if err != nil {
		return err
	}
	return atomicWriteFile(r.path, data)
}
