package main

import (
	"fmt"
	"os"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
		os.Exit(1)
	}

	prompter := newStdinPrompter()
	processor := NewProcessor(root, prompter)

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

	stats, skippedVideos := processor.Run()

	fmt.Println("\n--- Scan Complete ---")
	if len(skippedVideos) > 0 {
		fmt.Printf("\n--- SKIPPED VIDEOS (%d) ---\n", len(skippedVideos))
		for _, path := range skippedVideos {
			fmt.Println(path)
		}
	} else {
		fmt.Println("\nNo videos were skipped.")
	}

	_ = stats
}
