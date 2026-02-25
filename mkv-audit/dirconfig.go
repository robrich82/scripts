package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const dirConfigFile = "mkv-audit.json"

// DirConfig holds directory-level overrides loaded from mkv-audit.json.
type DirConfig struct {
	Overrides []DirOverride `json:"overrides"`
}

// DirOverride specifies IETF tag overrides for files under a given subdirectory.
// Path is relative to the scan root and matched case-insensitively.
//
// Example mkv-audit.json:
//
//	{
//	  "overrides": [
//	    {
//	      "path": "Agatha Christie's Poirot (1989)",
//	      "ietf": { "eng": "en-GB" }
//	    }
//	  ]
//	}
type DirOverride struct {
	Path string            `json:"path"` // relative directory path (case-insensitive prefix match)
	IETF map[string]string `json:"ietf"` // ISO 639-2/B code → IETF override, e.g. "eng": "en-GB"
}

// loadDirConfig reads mkv-audit.json from root. Returns nil if the file
// does not exist or is empty.
func loadDirConfig(root string) *DirConfig {
	data, err := os.ReadFile(filepath.Join(root, dirConfigFile))
	if err != nil {
		return nil
	}
	var cfg DirConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

// resolveIETFMap returns a copy of the global ietfMap with any applicable
// DirConfig overrides merged in. Overrides are applied in order; a later
// (more deeply nested) match can refine an earlier one.
func resolveIETFMap(filePath, root string, cfg *DirConfig) map[string]string {
	result := make(map[string]string, len(ietfMap))
	for k, v := range ietfMap {
		result[k] = v
	}
	if cfg == nil {
		return result
	}

	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return result
	}
	// Normalise to forward slashes for cross-platform path comparison.
	rel = filepath.ToSlash(rel)

	for _, override := range cfg.Overrides {
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
