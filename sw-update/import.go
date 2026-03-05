package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	reBookmarkLink   = regexp.MustCompile(`(?i)HREF="([^"]+)"[^>]*>([^<]+)</A>`)
	reGitHubReleases = regexp.MustCompile(`(?i)^https?://github\.com/([^/]+)/([^/?#]+)/releases`)
)

func cmdImport(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sw-update import <bookmarks.html>")
	}

	f, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("open %s: %w", args[0], err)
	}
	defer f.Close()

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Index existing app names (case-insensitive).
	existing := make(map[string]bool)
	for _, a := range cfg.Apps {
		existing[strings.ToLower(a.Name)] = true
	}

	var githubAdded, stubAdded, skipped int

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024) // 4MB line buffer for icon data

	for scanner.Scan() {
		line := scanner.Text()
		matches := reBookmarkLink.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			url := m[1]
			title := strings.TrimSpace(m[2])
			if title == "" {
				continue
			}

			if existing[strings.ToLower(title)] {
				skipped++
				continue
			}

			var app App
			if ghm := reGitHubReleases.FindStringSubmatch(url); ghm != nil {
				owner := ghm[1]
				repo := ghm[2]
				app = App{
					Name:    title,
					Enabled: true,
					URL:     url,
					Checker: CheckerConfig{
						Type:          "github",
						Repo:          owner + "/" + repo,
						Prerelease:    false,
						AssetPatterns: []string{},
					},
					Local: LocalConfig{Type: "none"},
				}
				githubAdded++
			} else {
				app = App{
					Name:    title,
					Enabled: false,
					URL:     url,
					Checker: CheckerConfig{
						Type:         "html",
						URL:          url,
						VersionRegex: "",
					},
					Local: LocalConfig{Type: "none"},
				}
				stubAdded++
			}

			cfg.Apps = append(cfg.Apps, app)
			existing[strings.ToLower(title)] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading bookmarks: %w", err)
	}

	if err := SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	dir, _ := configDir()
	fmt.Printf("Import complete — config saved to %s\n\n", dir)
	fmt.Printf("  %d GitHub apps added    (enabled, asset_patterns may need tuning)\n", githubAdded)
	fmt.Printf("  %d stub apps added      (disabled, need version_regex configured)\n", stubAdded)
	fmt.Printf("  %d skipped              (already in config)\n", skipped)
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  sw-update list          — review imported apps\n")
	fmt.Printf("  sw-update check         — check GitHub apps for updates\n")
	return nil
}
