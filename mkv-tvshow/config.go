package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// EpisodePair holds a [tmdbSeason, tmdbEpisode] remapping.
type EpisodePair [2]int

// SeasonOverride redirects a local season to a different TMDB show ID and/or season number.
type SeasonOverride struct {
	ID        string `json:"id"`
	MapSeason int    `json:"map_season"`
}

// ExtraEntry holds manual metadata for a non-episode file (extras, specials,
// bonus content) that does not carry a standard S##E## episode number.
type ExtraEntry struct {
	Date    string `json:"date,omitempty"` // YYYY-MM-DD; written as the MKV release date
	Season  int    `json:"season"`         // season number; used to select the season poster
	Episode int    `json:"episode,omitempty"` // if > 0, fetch metadata from this TMDB episode instead of using Date
}

// ManualEntry holds per-show TMDB ID overrides and episode-level remappings.
type ManualEntry struct {
	Overrides map[string]SeasonOverride `json:"overrides"` // keyed by season key e.g. "S02"
	Mapping   map[string]EpisodePair    `json:"mapping"`   // keyed by "S01E03" -> [tmdbSeason, tmdbEpisode]
	Extras    map[string]ExtraEntry     `json:"extras"`    // keyed by filename stem (no .mkv extension)
}

// LoadManualData reads per-show TMDB corrections from a JSON file.
// Returns an empty map (no error) if the file does not exist.
func LoadManualData(path string) (map[string]ManualEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ManualEntry{}, nil
		}
		return nil, err
	}
	var result map[string]ManualEntry
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// reverseChars maps Unicode lookalike characters back to their ASCII originals.
// These are used as filesystem-safe substitutes for characters illegal in filenames.
var reverseChars = []struct{ from, to string }{
	{"꞉", ":"},
	{"╱", "/"},
	{"╲", `\`},
	{"│", "|"},
	{"＊", "*"},
	{"？", "?"},
	{"''", `"`},
	{"＜", "<"},
	{"＞", ">"},
}

var yearSuffixRe = regexp.MustCompile(`\s\(\d{4}\)`)

// opEdRe matches an OP/ED/NCOP/NCED suffix (with optional number) preceded by
// whitespace at the end of a filename stem, e.g. " NCOP1", " ED", " OP2".
var opEdRe = regexp.MustCompile(`(?i)\s+((?:NC)?(?:OP|ED)\d*)$`)

// extractOpEdSuffix detects an OP/ED/NCOP/NCED suffix at the end of a
// filename stem. Returns (base, suffix) where suffix is uppercase (e.g.
// "NCOP1"), or (stem, "") if no suffix is found.
func extractOpEdSuffix(stem string) (base, suffix string) {
	m := opEdRe.FindStringSubmatchIndex(stem)
	if m == nil {
		return stem, ""
	}
	return stem[:m[0]], strings.ToUpper(stem[m[2]:m[3]])
}

// cleanMetadataTitle strips the file extension and restores ASCII characters
// from their Unicode filesystem-safe lookalikes.
func cleanMetadataTitle(nameOrFilename string) string {
	name := strings.TrimSuffix(nameOrFilename, filepath.Ext(nameOrFilename))
	for _, rc := range reverseChars {
		name = strings.ReplaceAll(name, rc.from, rc.to)
	}
	return name
}

// cleanSearchQuery prepares a folder name for use as a TMDB search query by
// stripping year suffixes, decorative brackets, and filesystem-safe lookalikes.
func cleanSearchQuery(folder string) string {
	name := yearSuffixRe.ReplaceAllString(folder, "")
	name = strings.Trim(name, "【】 ")
	return cleanMetadataTitle(name)
}
