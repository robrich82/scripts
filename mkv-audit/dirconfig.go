package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
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
	UndLang   string        `json:"und_lang,omitempty"` // ISO 639-2/B or IETF tag; last-resort language for "und" tracks
	Overrides []DirOverride `json:"overrides,omitempty"`
}

// DirOverride specifies per-directory overrides for files under a given subdirectory.
// Path is relative to the library root and matched case-insensitively.
type DirOverride struct {
	Path       string            `json:"path"`                  // relative directory path (case-insensitive prefix match); "." matches all
	IETF       map[string]string `json:"ietf,omitempty"`        // ISO 639-2/B code → IETF override, e.g. "eng": "en-GB"
	OriginLang string            `json:"origin_lang,omitempty"` // ISO 639-2/B code overriding TMDB original language, e.g. "jpn"
	UndLang    string            `json:"und_lang,omitempty"`    // ISO 639-2/B or IETF tag; last-resort language for "und" tracks
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

// saveGlobalConfig writes cfg to config.json in the mkv-audit config directory.
func saveGlobalConfig(cfg *GlobalConfig) error {
	cfgDir, err := configDir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(cfgDir, "config.json"), data)
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

// overrideMatches reports whether an override path applies to a relative file
// path. Both arguments must already be lowercased NFC strings.
// "." is a library-root wildcard that matches every file in the library.
// Otherwise the match is a case-insensitive prefix at a directory boundary.
func overrideMatches(relLower, ovLower string) bool {
	if ovLower == "." {
		return true
	}
	return relLower == ovLower || strings.HasPrefix(relLower, ovLower+"/")
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
	maps.Copy(result, ietfMap)

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
		// Normalize to NFC so config paths and filesystem paths with different
		// Unicode normalization forms (e.g. NFC vs NFD for Japanese characters)
		// compare equal.
		relLower := strings.ToLower(norm.NFC.String(rel))
		ovLower := strings.ToLower(norm.NFC.String(overridePath))
		if overrideMatches(relLower, ovLower) {
			maps.Copy(result, override.IETF)
		}
	}
	return result
}

// matchedOverrideIETF returns the merged IETF map from all DirOverrides in lib
// whose path is a case-insensitive prefix of filePath (relative to root).
// Returns nil when no overrides match. When multiple overrides match (e.g. a
// parent dir and a child dir), their IETF entries are merged in order so the
// most-specific override wins — mirroring resolveIETFMap behaviour.
func matchedOverrideIETF(filePath, root string, lib *Library) map[string]string {
	if lib == nil {
		return nil
	}
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	var merged map[string]string
	for _, override := range lib.Overrides {
		overridePath := filepath.ToSlash(override.Path)
		relLower := strings.ToLower(norm.NFC.String(rel))
		ovLower := strings.ToLower(norm.NFC.String(overridePath))
		if overrideMatches(relLower, ovLower) {
			if merged == nil {
				merged = make(map[string]string)
			}
			maps.Copy(merged, override.IETF)
		}
	}
	return merged
}

// resolveOriginLang returns the effective original-language ISO 639-2/B code for
// a file. Any matching DirOverride.OriginLang wins over the TMDB-derived value,
// letting you correct cases where TMDB reports the wrong original language
// (e.g. a Japanese show incorrectly listed as English).
func resolveOriginLang(filePath, root string, lib *Library, origin OriginInfo) string {
	if lib != nil {
		rel, err := filepath.Rel(root, filePath)
		if err == nil {
			rel = filepath.ToSlash(rel)
			var overrideLang string
			for _, override := range lib.Overrides {
				if override.OriginLang == "" {
					continue
				}
				overridePath := filepath.ToSlash(override.Path)
				relLower := strings.ToLower(norm.NFC.String(rel))
				ovLower := strings.ToLower(norm.NFC.String(overridePath))
				if overrideMatches(relLower, ovLower) {
					overrideLang = override.OriginLang
				}
			}
			if overrideLang != "" {
				return normalizeLanguage(overrideLang)
			}
		}
	}
	return origin.OrigLangISO()
}

// resolveUndLang returns the ISO 639-2/B code to use as a last-resort fallback
// for "und" tracks. Per-directory overrides (most specific wins) take priority
// over the library-level default. Returns "" when no fallback is configured.
func resolveUndLang(filePath, root string, lib *Library) string {
	if lib == nil {
		return ""
	}
	rel, err := filepath.Rel(root, filePath)
	if err == nil {
		rel = filepath.ToSlash(rel)
		var found string
		for _, override := range lib.Overrides {
			if override.UndLang == "" {
				continue
			}
			ovLower := strings.ToLower(norm.NFC.String(filepath.ToSlash(override.Path)))
			relLower := strings.ToLower(norm.NFC.String(rel))
			if overrideMatches(relLower, ovLower) {
				found = override.UndLang
			}
		}
		if found != "" {
			return normalizeLanguage(found)
		}
	}
	if lib.UndLang != "" {
		return normalizeLanguage(lib.UndLang)
	}
	return ""
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
