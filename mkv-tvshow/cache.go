package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// ShowCache is a persistent map from show folder name to confirmed TMDB show ID.
// IDs are stored as strings to accommodate both API-sourced integers and
// manually entered values. When loading a Python-generated cache (which stores
// integer IDs), numeric JSON values are automatically converted to strings.
type ShowCache struct {
	path string
	data map[string]string
}

// LoadCache reads the cache file at path. If the file does not exist an empty
// cache is returned without error.
func LoadCache(path string) (*ShowCache, error) {
	c := &ShowCache{path: path, data: make(map[string]string)}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return c, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Decode into interface{} first so we can handle both string and numeric IDs
	// (the Python version of this tool writes IDs as JSON numbers).
	var raw map[string]interface{}
	if err := json.NewDecoder(f).Decode(&raw); err != nil {
		return nil, err
	}
	for k, v := range raw {
		switch val := v.(type) {
		case string:
			c.data[k] = val
		case float64:
			c.data[k] = fmt.Sprintf("%d", int(val))
		}
	}
	return c, nil
}

// Get returns the cached TMDB ID for folder, and whether it was found.
func (c *ShowCache) Get(folder string) (string, bool) {
	id, ok := c.data[folder]
	return id, ok
}

// Set stores the TMDB ID for folder in memory. Call Save to persist.
func (c *ShowCache) Set(folder, id string) {
	c.data[folder] = id
}

// AllEntries returns a copy of all folder→tmdbID mappings in the cache.
func (c *ShowCache) AllEntries() map[string]string {
	out := make(map[string]string, len(c.data))
	for k, v := range c.data {
		out[k] = v
	}
	return out
}

// Save writes the cache to disk as indented JSON.
func (c *ShowCache) Save() error {
	data, err := json.MarshalIndent(c.data, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, data, 0644)
}
