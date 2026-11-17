package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"
)

// A file that fails a step is dropped from the retry list once it has failed
// at least maxRetryAttempts runs in a row and at least maxRetryDays days have
// passed since the first of them.
const (
	maxRetryAttempts = 5
	maxRetryDays     = 7
)

// retryNow is the retry list's clock; tests replace it.
var retryNow = time.Now

// retryToday is the date retryNow falls on, as local midnight.
func retryToday() time.Time {
	n := retryNow()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
}

// RetryList is a persistent set of file paths to re-process on every run
// regardless of mod-time cutoff: ones without a real air date yet, whose TMDB
// lookup failed, or whose tag write, poster or timestamps failed. Each entry
// counts the runs in a row that failed a step, from the date of the first;
// waiting on an air date, a failed TMDB lookup or missing mkvpropedit doesn't
// count.
type RetryList struct {
	path string
	data map[string]retryEntry // pathKey → entry
}

// retryEntry is one file on the retry list.
type retryEntry struct {
	path  string    // as last spelled, which Save writes
	fails int       // failed runs in a row
	since time.Time // date of the first of them; zero when fails is 0
}

// retryJSON is a retry entry as pending_dates.json holds it.
type retryJSON struct {
	Fails int    `json:"fails"`
	Since string `json:"since,omitempty"` // YYYY-MM-DD
}

// newRetryList returns an empty retry list that saves to path.
func newRetryList(path string) *RetryList {
	return &RetryList{path: path, data: make(map[string]retryEntry)}
}

// LoadRetryList reads the retry list from path. Returns an empty list without
// error if the file does not exist. Reads the current form
// ({"path": {"fails": n, "since": "YYYY-MM-DD"}}) and the older ones: a count
// per path, whose failed runs date from now, and an array of paths. Two
// spellings of one path merge into one entry with the higher count.
func LoadRetryList(path string) (*RetryList, error) {
	r := newRetryList(path)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return r, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries := map[string]retryEntry{}
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		var paths []string
		if err := json.Unmarshal(data, &paths); err != nil {
			return nil, err
		}
		for _, p := range paths {
			entries[p] = retryEntry{path: p}
		}
	} else {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		for p, v := range raw {
			e, err := parseRetryEntry(p, v)
			if err != nil {
				return nil, err
			}
			entries[p] = e
		}
	}
	// Sorted, so a merge keeps the same spelling on every load.
	for _, p := range slices.Sorted(maps.Keys(entries)) {
		k := pathKey(p)
		if e, ok := r.data[k]; !ok || entries[p].fails > e.fails {
			r.data[k] = entries[p]
		}
	}
	return r, nil
}

// parseRetryEntry reads path's entry, in either the object or the older count form.
func parseRetryEntry(path string, v json.RawMessage) (retryEntry, error) {
	e := retryEntry{path: path}
	if trimmed := bytes.TrimSpace(v); len(trimmed) == 0 || trimmed[0] != '{' {
		if err := json.Unmarshal(v, &e.fails); err != nil {
			return e, err
		}
		if e.fails > 0 {
			e.since = retryToday()
		}
		return e, nil
	}
	var j retryJSON
	if err := json.Unmarshal(v, &j); err != nil {
		return e, err
	}
	e.fails = j.Fails
	switch {
	case j.Since != "":
		since, err := time.ParseInLocation(time.DateOnly, j.Since, time.Local)
		if err != nil {
			return e, fmt.Errorf("%s: since: %w", path, err)
		}
		e.since = since
	case e.fails > 0:
		e.since = retryToday()
	}
	return e, nil
}

// Has reports whether path is in the retry list.
func (r *RetryList) Has(path string) bool {
	_, ok := r.data[pathKey(path)]
	return ok
}

// Add adds path to the retry list, keeping its failed runs if it is already there.
func (r *RetryList) Add(path string) {
	k := pathKey(path)
	e := r.data[k]
	e.path = path
	r.data[k] = e
}

// Keep keeps path on the retry list after a run in which no step failed,
// clearing its failed runs.
func (r *RetryList) Keep(path string) {
	r.data[pathKey(path)] = retryEntry{path: path}
}

// Fail records a run in which a step failed for path. Once path has failed
// maxRetryAttempts runs in a row over at least maxRetryDays days, it removes
// path and returns true, with the runs and the days they spanned.
func (r *RetryList) Fail(path string) (gaveUp bool, runs, days int) {
	k := pathKey(path)
	e := r.data[k]
	e.path = path
	e.fails++
	today := retryToday()
	if e.since.IsZero() || e.since.After(today) { // a later date means the clock was ahead
		e.since = today
	}
	if e.fails >= maxRetryAttempts && !today.Before(e.since.AddDate(0, 0, maxRetryDays)) {
		delete(r.data, k)
		return true, e.fails, daysBetween(e.since, today)
	}
	r.data[k] = e
	return false, e.fails, daysBetween(e.since, today)
}

// daysBetween is the number of calendar days from a to b, both local midnights.
func daysBetween(a, b time.Time) int {
	// Round, as a day across a DST change is 23 or 25 hours.
	return int((b.Sub(a) + 12*time.Hour) / (24 * time.Hour))
}

// Attempts returns the failed runs in a row recorded for path.
func (r *RetryList) Attempts(path string) int {
	return r.data[pathKey(path)].fails
}

// Move moves from's entry, with its failed runs, to the path to, and
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

// Remove removes path from the retry list.
func (r *RetryList) Remove(path string) {
	delete(r.data, pathKey(path))
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

// Save persists the retry list to disk as a JSON object of path → failed runs
// and the date of the first.
func (r *RetryList) Save() error {
	out := make(map[string]retryJSON, len(r.data))
	for _, e := range r.data {
		j := retryJSON{Fails: e.fails}
		if !e.since.IsZero() {
			j.Since = e.since.Format(time.DateOnly)
		}
		out[e.path] = j
	}
	data, err := json.MarshalIndent(out, "", "    ")
	if err != nil {
		return err
	}
	return atomicWriteFile(r.path, data)
}
