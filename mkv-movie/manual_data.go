package main

import (
	"encoding/json"
	"os"
)

// MovieManualEntry holds per-movie manual configuration overrides.
type MovieManualEntry struct {
	Deband       *bool  `json:"deband,omitempty"`
	Language     string `json:"language,omitempty"`      // ISO 639-1 code overriding TMDB original_language for Plex stream selection
	AltLanguage  string `json:"alt-language,omitempty"`  // overrides Language for the PLEX_ALT_TOKEN account
	Alt2Language string `json:"alt2-language,omitempty"` // overrides Language for the PLEX_ALT2_TOKEN account
}

// audioLangs returns the Plex audio language for each account. tmdbLang
// (TMDB's original_language) is the fallback when no "language" is configured,
// and each alt account uses that result unless it has its own override.
func (e MovieManualEntry) audioLangs(tmdbLang string) AudioLangs {
	def := e.Language
	if def == "" {
		def = tmdbLang
	}
	return AudioLangs{
		Default: def,
		Alts: map[string]string{
			altAccount:  e.AltLanguage,
			alt2Account: e.Alt2Language,
		},
	}
}

// LoadMovieManualData reads per-movie configuration from a JSON file keyed by
// filename stem (e.g. "Coco (2017)" for "Coco (2017).mkv").
// Returns an empty map (no error) if the file does not exist.
func LoadMovieManualData(path string) (map[string]MovieManualEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]MovieManualEntry{}, nil
		}
		return nil, err
	}
	var result map[string]MovieManualEntry
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}
