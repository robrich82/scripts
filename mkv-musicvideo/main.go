package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mkv-musicvideo"), nil
}

func main() {
	cutoffDateStr := flag.String("cutoff-date", "", "Only process files modified on or after this date (YYYY-MM-DD). Defaults to today.")
	flag.Parse()

	var cutoff time.Time
	if *cutoffDateStr != "" {
		var err error
		cutoff, err = time.Parse("2006-01-02", *cutoffDateStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --cutoff-date %q, expected YYYY-MM-DD\n", *cutoffDateStr)
			os.Exit(1)
		}
	} else {
		now := time.Now()
		cutoff = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}

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

	processor.cutoffDate = cutoff

	if cutoff.IsZero() {
		fmt.Println("--- Cutoff date: none (all files) ---")
	} else {
		fmt.Printf("--- Cutoff date: %s ---\n", cutoff.Format("2006-01-02"))
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
	if stats.SkippedOld > 0 {
		fmt.Printf("\nSkipped (old): %d\n", stats.SkippedOld)
	}
}
