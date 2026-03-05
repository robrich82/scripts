package main

import (
	"os"
	"path/filepath"
)

// configDir returns the per-user config directory for mkv-tvshow.
// On Windows this is %APPDATA%\mkv-tvshow; on other platforms $XDG_CONFIG_HOME/mkv-tvshow.
func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mkv-tvshow"), nil
}
