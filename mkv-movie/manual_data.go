package main

import (
	"encoding/json"
	"os"
)

// MovieManualEntry holds per-movie manual configuration overrides.
type MovieManualEntry struct {
	Deband *bool `json:"deband,omitempty"`
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
