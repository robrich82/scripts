package main

import (
	"fmt"
	"os"
)

func main() {
	mi, err := newRealMediaInfoReader()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: 'mediainfo' CLI not found. Please install it and add to PATH.")
		os.Exit(1)
	}

	syncer, err := newRealMKVSyncer()
	if err != nil {
		// mkvpropedit is optional — header sync will be skipped.
		fmt.Fprintln(os.Stderr, "Warning: 'mkvpropedit' not found. Internal header will not be updated.")
		syncer = nil
	}

	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
		os.Exit(1)
	}

	timer := &win32FileTimer{}

	fmt.Printf("Scanning %s...\n", dir)
	stats := walkAndProcess(dir, mi, syncer, timer)

	fmt.Printf("\nDone. Updated: %d  OK: %d  Skipped: %d  Errors: %d\n",
		stats.Updated, stats.OK, stats.Skipped, stats.Errors)
}
