package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// configDir returns the per-user config directory for mkv-audit.
// On Windows this is %APPDATA%\mkv-audit; on other platforms $XDG_CONFIG_HOME/mkv-audit.
func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mkv-audit"), nil
}

// GlobalConfig holds the full mkv-audit configuration stored in config.json
// inside the mkv-audit config directory.
type GlobalConfig struct {
	Libraries []Library `json:"libraries"`
}

// Library describes a media library root with its IETF tag overrides.
//
// Example config.json:
//
//	{
//	  "libraries": [
//	    {
//	      "name": "TV",
//	      "path": "G:\\TV",
//	      "overrides": [
//	        { "path": "Agatha Christie's Poirot (1989)", "ietf": { "eng": "en-GB" } }
//	      ]
//	    }
//	  ]
//	}
type Library struct {
	Name      string        `json:"name"`
	Type      string        `json:"type,omitempty"` // "tv" or "movie"; controls TMDB lookup strategy
	Path      string        `json:"path"`
	Overrides []DirOverride `json:"overrides,omitempty"`
}

// DirOverride specifies IETF tag overrides for files under a given subdirectory.
// Path is relative to the library root and matched case-insensitively.
type DirOverride struct {
	Path string            `json:"path"` // relative directory path (case-insensitive prefix match)
	IETF map[string]string `json:"ietf"` // ISO 639-2/B code → IETF override, e.g. "eng": "en-GB"
}

// loadGlobalConfig reads config.json from the mkv-audit config directory.
// Returns an empty config if the file does not exist.
func loadGlobalConfig() (*GlobalConfig, error) {
	cfgDir, err := configDir()
	if err != nil {
		return &GlobalConfig{}, nil
	}
	data, err := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	if os.IsNotExist(err) {
		return &GlobalConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg GlobalConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// detectLibrary finds the library whose Path is a prefix of cwd.
// If multiple libraries match, the most specific (longest path) wins.
// Returns nil if no library matches.
func detectLibrary(cwd string, cfg *GlobalConfig) *Library {
	cwd = filepath.Clean(cwd)
	var best *Library
	for i := range cfg.Libraries {
		libPath := filepath.Clean(cfg.Libraries[i].Path)
		rel, err := filepath.Rel(libPath, cwd)
		if err != nil {
			continue
		}
		// rel starting with ".." means cwd is not under libPath.
		if strings.HasPrefix(rel, "..") {
			continue
		}
		if best == nil || len(cfg.Libraries[i].Path) > len(best.Path) {
			lib := cfg.Libraries[i]
			best = &lib
		}
	}
	return best
}

// resolveIETFMap returns a copy of the global ietfMap with TMDB-derived origin
// info and any applicable Library overrides merged in, in that priority order:
//
//	global defaults < TMDB origin < manual library overrides
//
// origin is applied only for languages present in the global ietfMap, so
// unknown languages are never affected. Manual overrides win over TMDB.
func resolveIETFMap(filePath, root string, lib *Library, origin OriginInfo) map[string]string {
	result := make(map[string]string, len(ietfMap))
	for k, v := range ietfMap {
		result[k] = v
	}

	// TMDB-derived layer: override the original language's IETF tag with the
	// country-specific variant (e.g. "eng" → "en-CA" for a Canadian show).
	if tag := origin.IETFTag(); tag != "" {
		isoKey := origin.OrigLangISO()
		if _, ok := ietfMap[isoKey]; ok { // only for languages we already map
			result[isoKey] = tag
		}
	}

	if lib == nil {
		return result
	}

	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return result
	}
	// Normalise to forward slashes for cross-platform path comparison.
	rel = filepath.ToSlash(rel)

	for _, override := range lib.Overrides {
		overridePath := filepath.ToSlash(override.Path)
		// Case-insensitive prefix match. Ensure the match is at a directory
		// boundary (prefix ends with "/" or equals the relative path exactly).
		relLower := strings.ToLower(rel)
		ovLower := strings.ToLower(overridePath)
		if relLower == ovLower || strings.HasPrefix(relLower, ovLower+"/") {
			for k, v := range override.IETF {
				result[k] = v
			}
		}
	}
	return result
}

// contentRootDir returns the first path component of filePath relative to
// libraryRoot. This is the per-show or per-movie directory name used as the
// TMDB search key. Returns "" when filePath is not under libraryRoot.
func contentRootDir(filePath, libraryRoot string) string {
	rel, err := filepath.Rel(filepath.Clean(libraryRoot), filepath.Clean(filePath))
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	return parts[0]
}
