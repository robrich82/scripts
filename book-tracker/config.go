package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// dataDir returns the OS-appropriate directory for storing app data.
// Windows: %APPDATA%\book-tracker
// Other:   $XDG_CONFIG_HOME/book-tracker  (falls back to ~/.config/book-tracker)
func dataDir() string {
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "book-tracker")
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "book-tracker")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "book-tracker")
}

// seriesPath and completedPath return the JSON file paths.
func seriesPath(dir string) string    { return filepath.Join(dir, "series.json") }
func completedPath(dir string) string { return filepath.Join(dir, "completed.json") }
