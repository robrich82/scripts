package main

import (
	"encoding/json"
	"os"
)

// RetryList is a persistent set of file paths that used a placeholder air date
// and should be re-processed on every run regardless of mod-time cutoff until
// a real date is found on TMDB.
type RetryList struct {
	path string
	data map[string]bool
}

// LoadRetryList reads the retry list from path. Returns an empty list without
// error if the file does not exist.
func LoadRetryList(path string) (*RetryList, error) {
	r := &RetryList{path: path, data: make(map[string]bool)}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return r, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		return nil, err
	}
	for _, p := range paths {
		r.data[p] = true
	}
	return r, nil
}

// Has reports whether path is in the retry list.
func (r *RetryList) Has(path string) bool {
	return r.data[path]
}

// Add adds path to the retry list.
func (r *RetryList) Add(path string) {
	r.data[path] = true
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
		if _, err := os.Stat(path); os.IsNotExist(err) {
			delete(r.data, path)
			removed++
		}
	}
	return removed
}

// Save persists the retry list to disk as a JSON array.
func (r *RetryList) Save() error {
	paths := make([]string, 0, len(r.data))
	for p := range r.data {
		paths = append(paths, p)
	}
	data, err := json.MarshalIndent(paths, "", "    ")
	if err != nil {
		return err
	}
	return atomicWriteFile(r.path, data)
}
