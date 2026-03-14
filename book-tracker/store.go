package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadStore reads series.json and completed.json from dir.
// Missing files are treated as empty lists.
func LoadStore(dir string) (*Store, error) {
	st := &Store{}

	if err := loadJSON(seriesPath(dir), &st.Series); err != nil {
		return nil, fmt.Errorf("loading series: %w", err)
	}
	if err := loadJSON(completedPath(dir), &st.Completed); err != nil {
		return nil, fmt.Errorf("loading completed: %w", err)
	}
	if st.Series == nil {
		st.Series = []*Series{}
	}
	if st.Completed == nil {
		st.Completed = []*CompletedSeries{}
	}
	return st, nil
}

// SaveStore writes both JSON files atomically (write to temp, rename).
func SaveStore(dir string, st *Store) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := saveJSON(seriesPath(dir), st.Series); err != nil {
		return fmt.Errorf("saving series: %w", err)
	}
	if err := saveJSON(completedPath(dir), st.Completed); err != nil {
		return fmt.Errorf("saving completed: %w", err)
	}
	return nil
}

func loadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// newID generates a short unique ID based on the name.
// Collisions are extremely unlikely for a personal list of <1000 entries.
func newID(name string) string {
	// Use first 8 chars of a simple hash for readability.
	h := uint32(2166136261)
	for _, c := range name {
		h ^= uint32(c)
		h *= 16777619
	}
	// Append a random suffix to avoid collisions between similar names.
	return fmt.Sprintf("%08x", h)
}

// slugID creates a unique ID; if the simple hash collides, append a counter.
func uniqueID(st *Store, name string) string {
	base := newID(name)
	id := base
	for i := 1; ; i++ {
		clash := false
		for _, s := range st.Series {
			if s.ID == id {
				clash = true
				break
			}
		}
		if !clash {
			for _, c := range st.Completed {
				if c.ID == id {
					clash = true
					break
				}
			}
		}
		if !clash {
			return id
		}
		id = fmt.Sprintf("%s%d", base, i)
	}
}
