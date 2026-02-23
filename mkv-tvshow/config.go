package main

import (
	"path/filepath"
	"regexp"
	"strings"
)

// EpisodePair holds a [tmdbSeason, tmdbEpisode] remapping.
type EpisodePair [2]int

// SeasonOverride redirects a local season to a different TMDB show ID and/or season number.
type SeasonOverride struct {
	ID        string
	MapSeason int
}

// ManualEntry holds per-show TMDB ID overrides and episode-level remappings.
type ManualEntry struct {
	Overrides map[string]SeasonOverride // keyed by season key e.g. "S02"
	Mapping   map[string]EpisodePair   // keyed by "S01E03" -> [tmdbSeason, tmdbEpisode]
}

// manualData contains hardcoded per-show corrections for shows whose TMDB
// structure doesn't match the local file naming.
var manualData = map[string]ManualEntry{
	"Panty & Stocking with Garterbelt": {
		Overrides: map[string]SeasonOverride{
			"S02": {ID: "246862", MapSeason: 1},
		},
		Mapping: map[string]EpisodePair{
			"S00E01": {0, 1}, "S01E01": {1, 1}, "S01E03": {1, 3},
			"S01E05": {1, 5}, "S01E07": {1, 7}, "S01E09": {1, 9},
			"S01E11": {1, 11}, "S01E12": {1, 12}, "S01E14": {1, 14},
			"S01E16": {1, 16}, "S01E18": {1, 18}, "S01E21": {1, 23},
			"S01E23": {1, 25}, "S01E25": {1, 27},
		},
	},
	"Full Metal Panic!": {
		Mapping: map[string]EpisodePair{
			"S02E01": {0, 1}, "S02E02": {0, 2}, "S02E03": {2, 2},
			"S02E04": {2, 3}, "S02E05": {2, 5}, "S02E06": {2, 4},
			"S02E07": {2, 6}, "S02E08": {2, 7}, "S02E09": {2, 8},
			"S02E10": {2, 9}, "S02E11": {2, 10}, "S02E12": {2, 11},
			"S02E14": {0, 3}, "S03E14": {0, 4},
		},
	},
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
