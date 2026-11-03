package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// CheckerConfig describes how to check for a new upstream version.
type CheckerConfig struct {
	Type              string   `json:"type"`                          // "github", "html", "sourceforge", or "http-header"
	Repo              string   `json:"repo,omitempty"`                // owner/repo (github)
	Prerelease        bool     `json:"prerelease,omitempty"`          // include pre-releases (github)
	AssetPatterns     []string `json:"asset_patterns,omitempty"`      // glob/regex patterns for assets (github, html, sourceforge)
	AssetSort         string   `json:"asset_sort,omitempty"`          // sort matched assets: "name" (asc) or "name_desc" (desc), then take first
	URL               string   `json:"url,omitempty"`                 // page URL (html) or resource URL (http-header)
	AssetPage         string   `json:"asset_page,omitempty"`          // page to scrape for assets when url has no download links (html)
	VersionRegex      string   `json:"version_regex,omitempty"`       // regex to extract version (html, sourceforge)
	Project           string   `json:"project,omitempty"`             // project slug (sourceforge)
	SFPath            string   `json:"sf_path,omitempty"`             // path filter for RSS, e.g. "/munt" (sourceforge)
	Header            string   `json:"header,omitempty"`              // HTTP header to use as version (http-header, default: Last-Modified)
	VersionRewrite    []string `json:"version_rewrite,omitempty"`     // [regex, replacement] applied after version extraction
	VersionDateLayout []string `json:"version_date_layout,omitempty"` // [parse-layout, format-layout] Go time layouts to normalise a date version
	RequireAsset      bool     `json:"require_asset,omitempty"`       // skip releases with no matching asset_patterns (github)
	VersionCompare    string   `json:"version_compare,omitempty"`     // "" (default: ordinal) or "hash" (git short-hash prefix match)
}

// LocalConfig describes how to detect the locally installed version.
type LocalConfig struct {
	Type              string   `json:"type"`                          // "none", "exe", "file-version"
	Path              string   `json:"path,omitempty"`                // exe or file path
	Args              []string `json:"args,omitempty"`                // args for exe detector
	VersionField      string   `json:"version_field,omitempty"`       // PE field to read: "FileVersion" (default) or "ProductVersion"
	VersionRegex      string   `json:"version_regex,omitempty"`       // regex to extract version from output
	VersionRewrite    []string `json:"version_rewrite,omitempty"`     // [regex, replacement] applied after extraction
	VersionDateLayout []string `json:"version_date_layout,omitempty"` // [parse-layout, format-layout] Go time layouts to normalise a date version
}

// UpdateConfig describes a custom command used to update an app in place,
// instead of downloading and running a release asset (e.g. "yt-dlp.exe -U",
// "pip install --upgrade <pkg>", or a self-updater script).
type UpdateConfig struct {
	Command  string   `json:"command"`            // executable or script to run
	Args     []string `json:"args,omitempty"`     // arguments passed to the command
	Elevated bool     `json:"elevated,omitempty"` // run with a UAC elevation prompt (for tools that write to Program Files)
}

// App represents a single tracked application.
type App struct {
	Name    string        `json:"name"`
	Enabled bool          `json:"enabled"`
	URL     string        `json:"url,omitempty"` // original bookmark URL (for reference)
	Checker CheckerConfig `json:"checker"`
	Local   LocalConfig   `json:"local"`
	Update  *UpdateConfig `json:"update,omitempty"` // custom in-place update command (optional)

	ArchivePassword string `json:"archive_password,omitempty"` // decrypts a password-protected .zip/.7z asset
}

// Config is the top-level configuration file.
type Config struct {
	DownloadDir string `json:"download_dir,omitempty"`
	Apps        []App  `json:"apps"`
}

// configDir returns the directory where config and state files are stored.
func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sw-update"), nil
}

// LoadConfig loads the config from disk, returning an empty config if none exists.
func LoadConfig() (*Config, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "sw-update.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{Apps: []App{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveConfig writes the config to disk.
func SaveConfig(cfg *Config) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "sw-update.json")
	data, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0644)
}

// FindApp returns a pointer to the app with the given name, or nil.
func (cfg *Config) FindApp(name string) *App {
	for i := range cfg.Apps {
		if cfg.Apps[i].Name == name {
			return &cfg.Apps[i]
		}
	}
	return nil
}

// DefaultDownloadDir returns the configured download dir, or ~/Downloads/sw-update.
func (cfg *Config) DefaultDownloadDir() (string, error) {
	if cfg.DownloadDir != "" {
		return cfg.DownloadDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads", "sw-update"), nil
}
