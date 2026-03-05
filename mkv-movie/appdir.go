package main

import (
	"os"
	"path/filepath"
)

// configDir returns the per-user config directory for mkv-movie.
// On Windows this is %APPDATA%\mkv-movie; on other platforms $XDG_CONFIG_HOME/mkv-movie.
func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mkv-movie"), nil
}
