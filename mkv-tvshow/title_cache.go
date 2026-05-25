package main

import (
	"encoding/json"
	"os"
)

// TitleCache stores the full MKV title for files whose title was explicitly set
// by --title-check. The full title may be longer than the filename (which is
// constrained by MAX_PATH) because MKV metadata has no path-length limit.
//
// The normal processing mode reads this cache before computing a title from the
// filename, so it will not overwrite an extended title with a truncated one.
type TitleCache struct {
	path   string
	Titles map[string]string // absolute filepath → full MKV title
}

// LoadTitleCache reads the title cache from path.
// Returns an empty cache (no error) if the file does not exist.
func LoadTitleCache(path string) (*TitleCache, error) {
	tc := &TitleCache{path: path, Titles: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return tc, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &tc.Titles); err != nil {
		return nil, err
	}
	return tc, nil
}

// Get returns the cached full MKV title for a file, if any.
func (tc *TitleCache) Get(filePath string) (string, bool) {
	if tc == nil {
		return "", false
	}
	t, ok := tc.Titles[filePath]
	return t, ok
}

// Set stores the full MKV title for a file.
func (tc *TitleCache) Set(filePath, title string) {
	if tc == nil {
		return
	}
	tc.Titles[filePath] = title
}

// CleanStalePaths removes entries whose file paths no longer exist on disk.
// Returns the number of entries removed.
func (tc *TitleCache) CleanStalePaths() int {
	removed := 0
	for path := range tc.Titles {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			delete(tc.Titles, path)
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
	data, err := json.MarshalIndent(tc.Titles, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tc.path, data, 0644)
}
