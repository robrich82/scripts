package main

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
)

// TitleCache stores the full MKV title for files whose title --title-check set
// or found already in the MKV (a normal run updates its episode code after a
// multi-episode rename). The full title may be longer than the filename (which
// is constrained by MAX_PATH) because MKV metadata has no path-length limit.
//
// The normal processing mode reads this cache before computing a title from the
// filename, so it will not overwrite an extended title with a truncated one.
type TitleCache struct {
	path   string
	titles map[string]titleEntry // pathKey → entry
}

// titleEntry is one file's full title.
type titleEntry struct {
	path  string // as last spelled, which Save writes
	title string
}

// LoadTitleCache reads the title cache from path.
// Returns an empty cache (no error) if the file does not exist. Of two
// spellings of one path, the one that sorts first is kept.
func LoadTitleCache(path string) (*TitleCache, error) {
	tc := &TitleCache{path: path, titles: map[string]titleEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return tc, nil
		}
		return nil, err
	}
	var titles map[string]string
	if err := json.Unmarshal(data, &titles); err != nil {
		return nil, err
	}
	for _, p := range slices.Sorted(maps.Keys(titles)) {
		if _, ok := tc.titles[pathKey(p)]; !ok {
			tc.titles[pathKey(p)] = titleEntry{path: p, title: titles[p]}
		}
	}
	return tc, nil
}

// Get returns the cached full MKV title for a file, if any.
func (tc *TitleCache) Get(filePath string) (string, bool) {
	if tc == nil {
		return "", false
	}
	e, ok := tc.titles[pathKey(filePath)]
	return e.title, ok
}

// Set stores the full MKV title for a file.
func (tc *TitleCache) Set(filePath, title string) {
	if tc == nil {
		return
	}
	tc.titles[pathKey(filePath)] = titleEntry{path: filePath, title: title}
}

// Move moves from's title to the path to, and reports whether from had one.
func (tc *TitleCache) Move(from, to string) bool {
	if tc == nil {
		return false
	}
	e, ok := tc.titles[pathKey(from)]
	if !ok {
		return false
	}
	delete(tc.titles, pathKey(from))
	e.path = to
	tc.titles[pathKey(to)] = e
	return true
}

// Delete removes filePath's title, if any.
func (tc *TitleCache) Delete(filePath string) {
	if tc == nil {
		return
	}
	delete(tc.titles, pathKey(filePath))
}

// CleanStalePaths removes entries whose file paths no longer exist on disk.
// Returns the number of entries removed.
func (tc *TitleCache) CleanStalePaths() int {
	removed := 0
	for k, e := range tc.titles {
		if pathGone(e.path) {
			delete(tc.titles, k)
			removed++
		}
	}
	return removed
}

// Save writes the cache to disk.
func (tc *TitleCache) Save() error {
	if tc == nil {
		return nil
	}
	out := make(map[string]string, len(tc.titles))
	for _, e := range tc.titles {
		out[e.path] = e.title
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(tc.path, data)
}
