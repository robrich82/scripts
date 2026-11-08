package main

import (
	"os"
	"path/filepath"
)

// pathGone reports whether path is missing from a volume that is itself
// reachable, so an offline drive or share doesn't make its files look deleted.
func pathGone(path string) bool {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return false
	}
	_, err := os.Stat(filepath.VolumeName(path) + string(filepath.Separator))
	return err == nil
}
