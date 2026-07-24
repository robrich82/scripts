package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// accountLangEntry is the subset of an mkv-tvshow / mkv-movie manual_data.json
// entry that mkv-audit cares about: the audio language each Plex account
// prefers for that title. Both tools use the same key names, so one shape
// covers both files. All other fields in the entry are ignored.
type accountLangEntry struct {
	Language     string `json:"language"`
	AltLanguage  string `json:"alt-language"`
	Alt2Language string `json:"alt2-language"`
}

// AccountLangs maps a manual_data.json key — the show folder name for TV, the
// filename stem for movies — to the ISO 639-2/B audio languages configured for
// the Plex accounts. Someone actually watches in each of these languages, so
// trim must keep their audio even when it is not the original language.
type AccountLangs map[string][]string

// manualDataTool returns the sibling tool whose manual_data.json describes a
// library of the given type, or "" when the type is unset or unrecognised.
func manualDataTool(libType string) string {
	switch strings.ToLower(libType) {
	case "tv":
		return "mkv-tvshow"
	case "movie":
		return "mkv-movie"
	}
	return ""
}

// loadAccountLangs reads per-title audio language preferences from the sibling
// tool's manual_data.json (%APPDATA%\mkv-tvshow or %APPDATA%\mkv-movie) — the
// same "language" / "alt-language" / "alt2-language" keys those tools use to
// set each Plex account's preferred audio language.
//
// Returns nil when the library type is unset, the file is absent, or it cannot
// be parsed; trim then falls back to the original language alone. A missing
// file is normal (not every library is managed by a sibling tool), so it is not
// reported as an error.
func loadAccountLangs(libType string) AccountLangs {
	tool := manualDataTool(libType)
	if tool == "" {
		return nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(base, tool, "manual_data.json"))
	if err != nil {
		return nil
	}
	return parseAccountLangs(data)
}

// parseAccountLangs decodes manual_data.json content into the per-title
// language sets, normalising each ISO 639-1 code to ISO 639-2/B and dropping
// titles with no language configured. Returns nil on malformed JSON.
func parseAccountLangs(data []byte) AccountLangs {
	var raw map[string]accountLangEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	out := make(AccountLangs, len(raw))
	for key, entry := range raw {
		var langs []string
		for _, code := range []string{entry.Language, entry.AltLanguage, entry.Alt2Language} {
			if code == "" {
				continue
			}
			iso := normalizeLanguage(code)
			if iso == "" || iso == "und" || slices.Contains(langs, iso) {
				continue
			}
			langs = append(langs, iso)
		}
		if len(langs) > 0 {
			out[key] = langs
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// forFile returns the account languages configured for a media file. The
// filename stem is tried first, matching mkv-movie's keys and covering flat
// movie libraries where the file sits directly in the library root; then the
// content root directory, matching mkv-tvshow's show folders and movies kept in
// a folder of their own. Returns nil when the title has no entry.
func (a AccountLangs) forFile(filePath, libRoot string) []string {
	if len(a) == 0 {
		return nil
	}
	base := filepath.Base(filePath)
	if langs, ok := a[strings.TrimSuffix(base, filepath.Ext(base))]; ok {
		return langs
	}
	return a[contentRootDir(filePath, libRoot)]
}
