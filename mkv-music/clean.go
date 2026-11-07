package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func runClean(root string) {
	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}

	ignoreFile := filepath.Join(cfgDir, "ignore_patterns.json")

	b, err := os.ReadFile(ignoreFile)
	if os.IsNotExist(err) {
		fmt.Println("No ignore patterns file found; nothing to clean.")
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading ignore patterns: %v\n", err)
		os.Exit(1)
	}

	var data IgnoreData
	if err := json.Unmarshal(b, &data); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing ignore patterns: %v\n", err)
		os.Exit(1)
	}

	if len(data.FilePatterns) == 0 {
		fmt.Println("No file-specific patterns found; nothing to clean.")
		return
	}

	removed := removeStalePatterns(&data, root)
	if removed == 0 {
		fmt.Println("File patterns are clean; nothing to remove.")
		return
	}

	if err := writeIgnoreFile(ignoreFile, data); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving ignore patterns: %v\n", err)
		os.Exit(1)
	}

	suffix := "ies"
	if removed == 1 {
		suffix = "y"
	}
	fmt.Printf("\nRemoved %d stale file-pattern entr%s.\n", removed, suffix)
}

// removeStalePatterns deletes the file-pattern entries in data whose video no
// longer exists and returns how many it removed. An entry it can't confirm is
// gone, because its drive is offline or part of root is unreadable, is kept.
func removeStalePatterns(data *IgnoreData, root string) int {
	// For legacy base-name keys, walk root to collect existing .mkv file names.
	// Newer entries store absolute paths and are checked directly via os.Stat.
	mkvFiles := make(map[string]bool)
	scanned := true
	hasLegacyKeys := false
	for name := range data.FilePatterns {
		if !filepath.IsAbs(name) {
			hasLegacyKeys = true
			break
		}
	}
	if hasLegacyKeys {
		// The callback never returns an error, so neither does WalkDir.
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				fmt.Printf("[WARN] Could not read %s, keeping legacy entries: %v\n", path, err)
				scanned = false
				return nil
			}
			if !d.IsDir() && filepath.Ext(d.Name()) == ".mkv" {
				mkvFiles[d.Name()] = true
			}
			return nil
		})
	}

	removed := 0
	for name := range data.FilePatterns {
		var stale bool
		if filepath.IsAbs(name) {
			stale = videoGone(name)
		} else {
			stale = scanned && !mkvFiles[name]
		}
		if stale {
			fmt.Printf("Removing stale file-pattern entry: %s\n", name)
			delete(data.FilePatterns, name)
			removed++
		}
	}
	return removed
}

// videoGone reports whether path is missing from a volume that is itself
// reachable, so an offline drive or share doesn't make its videos look deleted.
func videoGone(path string) bool {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return false
	}
	_, err := os.Stat(filepath.VolumeName(path) + string(filepath.Separator))
	return err == nil
}
