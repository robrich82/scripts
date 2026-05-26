package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// EpisodePair holds a TMDB remapping for an episode.
//
// Two-element form [season, episode]: remaps a single local episode.
// Three-or-more element form [season, ep1, ep2, ...]: used by --title-check
// to list explicit TMDB episode numbers when a local range file covers
// non-consecutive TMDB episodes (e.g. S01E18-E20 → TMDB [18, 20, 22]).
// The processor always uses only the first two elements.
type EpisodePair []int

// SeasonOverride redirects a local season to a different TMDB show ID and/or season number.
type SeasonOverride struct {
	ID        string `json:"id"`
	MapSeason int    `json:"map_season"`
}

// ExtraEntry holds manual metadata for a non-episode file (extras, specials,
// bonus content) that does not carry a standard S##E## episode number.
type ExtraEntry struct {
	Date    string `json:"date,omitempty"`    // YYYY-MM-DD; if set, overrides episode air date
	Season  int    `json:"season,omitempty"`  // poster season; defaults to episode's season when episode is set
	Episode string `json:"episode,omitempty"` // "S##E##" e.g. "S01E03"; fetch metadata from this episode
}

// ManualEntry holds per-show TMDB ID overrides and episode-level remappings.
type ManualEntry struct {
	Overrides      map[string]SeasonOverride `json:"overrides"`                 // keyed by season key e.g. "S02"
	Mapping        map[string]EpisodePair    `json:"mapping"`                   // keyed by "S01E03" -> [tmdbSeason, tmdbEpisode]
	Extras         map[string]ExtraEntry     `json:"extras"`                    // keyed by filename stem (no .mkv extension)
	TitleOverrides map[string]string         `json:"title_overrides"`           // keyed by "S01E03" -> correct FS-safe title
	Language       string                    `json:"language,omitempty"`        // ISO 639-1 code overriding TMDB original_language for Plex audio preference
	Deband         *bool                     `json:"deband,omitempty"`          // show-level deband flag
	DebandSeasons  map[string]bool           `json:"deband_seasons,omitempty"`  // e.g. "S01": true
	DebandEpisodes map[string]bool           `json:"deband_episodes,omitempty"` // e.g. "S01E05": true
}

// hasDebandConfig reports whether any deband configuration exists for the
// given keys. It distinguishes "explicitly configured" (true or false) from
// "not mentioned at all" so the caller can decide whether to skip the file.
func (e ManualEntry) hasDebandConfig(seasonKey, episodeKey string) bool {
	if episodeKey != "" {
		if _, ok := e.DebandEpisodes[episodeKey]; ok {
			return true
		}
	}
	if seasonKey != "" {
		if _, ok := e.DebandSeasons[seasonKey]; ok {
			return true
		}
	}
	return e.Deband != nil
}

// resolveDeband returns whether debanding should be applied for the given
// season and episode keys. Episode-level takes precedence over season-level,
// which takes precedence over the show-level flag. seasonKey format: "S01";
// episodeKey format: "S01E05". Pass an empty episodeKey for extras lookups.
func (e ManualEntry) resolveDeband(seasonKey, episodeKey string) bool {
	if episodeKey != "" {
		if v, ok := e.DebandEpisodes[episodeKey]; ok {
			return v
		}
	}
	if seasonKey != "" {
		if v, ok := e.DebandSeasons[seasonKey]; ok {
			return v
		}
	}
	if e.Deband != nil {
		return *e.Deband
	}
	return false
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
	{"＂", `"`},
	{"＜", "<"},
	{"＞", ">"},
}

var yearSuffixRe = regexp.MustCompile(`\s\(\d{4}\)`)

// opEdRe matches an OP/ED/NCOP/NCED suffix (with optional number) preceded by
// whitespace or a hyphen at the end of a filename stem, e.g. " NCOP1",
// "-NCED2", " ED", "-OP2".
var opEdRe = regexp.MustCompile(`(?i)(?:\s+|-)((?:NC)?(?:OP|ED)\d*)$`)

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

var episodeRefRe = regexp.MustCompile(`(?i)^S(\d+)E(\d+)$`)

// parseEpisodeRef parses a "S##E##" string into season and episode numbers.
// Returns (0, 0, false) if the string does not match the expected format.
func parseEpisodeRef(s string) (season, episode int, ok bool) {
	m := episodeRefRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	sn, _ := strconv.Atoi(m[1])
	en, _ := strconv.Atoi(m[2])
	return sn, en, true
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
