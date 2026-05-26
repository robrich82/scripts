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

	// For legacy base-name keys, walk root to collect existing .mkv file names.
	// Newer entries store absolute paths and are checked directly via os.Stat.
	mkvFiles := make(map[string]bool)
	hasLegacyKeys := false
	for name := range data.FilePatterns {
		if !filepath.IsAbs(name) {
			hasLegacyKeys = true
			break
		}
	}
	if hasLegacyKeys {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && filepath.Ext(d.Name()) == ".mkv" {
				mkvFiles[d.Name()] = true
			}
			return nil
		})
	}

	removed := 0
	for name := range data.FilePatterns {
		var stale bool
		if filepath.IsAbs(name) {
			_, err := os.Stat(name)
			stale = os.IsNotExist(err)
		} else {
			stale = !mkvFiles[name]
		}
		if stale {
			fmt.Printf("Removing stale file-pattern entry: %s\n", name)
			delete(data.FilePatterns, name)
			removed++
		}
	}

	if removed == 0 {
		fmt.Println("File patterns are clean; nothing to remove.")
		return
	}

	updated, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshalling ignore patterns: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(ignoreFile, updated, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving ignore patterns: %v\n", err)
		os.Exit(1)
	}

	suffix := "ies"
	if removed == 1 {
		suffix = "y"
	}
	fmt.Printf("\nRemoved %d stale file-pattern entr%s.\n", removed, suffix)
}
