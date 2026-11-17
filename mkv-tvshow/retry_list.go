package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"runtime"
	"slices"
	"strings"
)

// maxRetryAttempts is how many runs in a row a file may fail a step before
// the retry list gives up on it.
const maxRetryAttempts = 5

// foldPathCase is whether retry-list paths compare case-insensitively, as
// they do on Windows; tests set it.
var foldPathCase = runtime.GOOS == "windows"

// pathKey is p in the form retry-list lookups compare.
func pathKey(p string) string {
	if foldPathCase {
		return strings.ToLower(p)
	}
	return p
}

// RetryList is a persistent set of file paths to re-process on every run
// regardless of mod-time cutoff: ones without a real air date yet, whose TMDB
// lookup failed, or whose tag write, poster or timestamps failed. Each entry
// counts the runs in a row that failed a step; waiting on an air date, a
// failed TMDB lookup or missing mkvpropedit doesn't count.
type RetryList struct {
	path string
	data map[string]retryEntry // pathKey → entry
}

// retryEntry is one file on the retry list.
type retryEntry struct {
	path  string // as last spelled, which Save writes
	fails int    // failed runs in a row
}

// newRetryList returns an empty retry list that saves to path.
func newRetryList(path string) *RetryList {
	return &RetryList{path: path, data: make(map[string]retryEntry)}
}

// LoadRetryList reads the retry list from path. Returns an empty list without
// error if the file does not exist. Reads both the current object form
// ({"path": attempts}) and the older array of paths. Two spellings of one
// path merge into one entry with the higher count.
func LoadRetryList(path string) (*RetryList, error) {
	r := newRetryList(path)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return r, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries := map[string]int{}
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		var paths []string
		if err := json.Unmarshal(data, &paths); err != nil {
			return nil, err
		}
		for _, p := range paths {
			entries[p] = 0
		}
	} else if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	// Sorted, so a merge keeps the same spelling on every load.
	for _, p := range slices.Sorted(maps.Keys(entries)) {
		k := pathKey(p)
		if e, ok := r.data[k]; !ok || entries[p] > e.fails {
			r.data[k] = retryEntry{path: p, fails: entries[p]}
		}
	}
	return r, nil
}

// Has reports whether path is in the retry list.
func (r *RetryList) Has(path string) bool {
	_, ok := r.data[pathKey(path)]
	return ok
}

// Add adds path to the retry list, keeping its failed-run count if it is
// already there.
func (r *RetryList) Add(path string) {
	k := pathKey(path)
	e := r.data[k]
	e.path = path
	r.data[k] = e
}

// Keep keeps path on the retry list after a run in which no step failed,
// clearing its failed-run count.
func (r *RetryList) Keep(path string) {
	r.data[pathKey(path)] = retryEntry{path: path}
}

// Fail records a run in which a step failed for path. After maxRetryAttempts
// such runs in a row it removes path and returns true.
func (r *RetryList) Fail(path string) (gaveUp bool) {
	k := pathKey(path)
	e := r.data[k]
	e.path = path
	e.fails++
	if e.fails >= maxRetryAttempts {
		delete(r.data, k)
		return true
	}
	r.data[k] = e
	return false
}

// Attempts returns the failed runs in a row recorded for path.
func (r *RetryList) Attempts(path string) int {
	return r.data[pathKey(path)].fails
}

// Remove removes path from the retry list.
func (r *RetryList) Remove(path string) {
	delete(r.data, pathKey(path))
}

// Move moves from's entry, with its failed-run count, to the path to, and
// reports whether from was on the list.
func (r *RetryList) Move(from, to string) bool {
	e, ok := r.data[pathKey(from)]
	if !ok {
		return false
	}
	delete(r.data, pathKey(from))
	e.path = to
	r.data[pathKey(to)] = e
	return true
}

// Len returns the number of entries in the retry list.
func (r *RetryList) Len() int {
	return len(r.data)
}

// CleanStalePaths removes entries for file paths that no longer exist on disk.
// Returns the number of entries removed.
func (r *RetryList) CleanStalePaths() int {
	removed := 0
	for k, e := range r.data {
		if pathGone(e.path) {
			delete(r.data, k)
			removed++
		}
	}
	return removed
}

// Save persists the retry list to disk as a JSON object of path → failed runs.
func (r *RetryList) Save() error {
	out := make(map[string]int, len(r.data))
	for _, e := range r.data {
		out[e.path] = e.fails
	}
	data, err := json.MarshalIndent(out, "", "    ")
	if err != nil {
		return err
	}
	return atomicWriteFile(r.path, data)
}
