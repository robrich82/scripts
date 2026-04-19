package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mkv-musicvideo"), nil
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
		os.Exit(1)
	}

	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating config dir: %v\n", err)
		os.Exit(1)
	}
	ignoreFile := filepath.Join(cfgDir, "ignore_patterns.json")

	prompter := newStdinPrompter()
	processor := NewProcessor(root, ignoreFile, prompter)

	if err := processor.LoadIgnorePatterns(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load ignore patterns: %v\n", err)
	}

	tagger, err := newRealMusicVideoTagger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v — tag copying disabled\n", err)
	} else {
		processor.tagger = tagger
	}

	fmt.Printf("--- Scanning and Matching in: %s ---\n\n", root)

	stats, skippedVideos, unmatchedVideos := processor.Run()

	fmt.Println("\n--- Scan Complete ---")
	if len(unmatchedVideos) > 0 {
		fmt.Printf("\n--- UNMATCHED VIDEOS (%d) ---\n", len(unmatchedVideos))
		for _, path := range unmatchedVideos {
			fmt.Println(path)
		}
	}
	if len(skippedVideos) > 0 {
		fmt.Printf("\n--- SKIPPED VIDEOS (%d) ---\n", len(skippedVideos))
		for _, path := range skippedVideos {
			fmt.Println(path)
		}
	}
	if len(unmatchedVideos) == 0 && len(skippedVideos) == 0 {
		fmt.Println("\nNo videos were skipped or unmatched.")
	}

	_ = stats
}
