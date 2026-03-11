package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func cmdInstall(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sw-update install <name>")
	}
	name := args[0]

	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	app := cfg.FindApp(name)
	if app == nil {
		return fmt.Errorf("app %q not found", name)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	checker := NewChecker(client)

	fmt.Printf("Checking latest version of %s...\n", name)
	result, err := checker.Check(*app)
	if err != nil {
		return fmt.Errorf("check %s: %w", name, err)
	}
	fmt.Printf("Latest version: %s\n", result.Version)

	if len(result.Assets) == 0 {
		if app.Checker.URL != "" {
			fmt.Printf("No downloadable assets found. Visit: %s\n", app.Checker.URL)
		} else {
			fmt.Printf("No downloadable assets found.\n")
		}
		return nil
	}

	downloadDir, err := cfg.DefaultDownloadDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(downloadDir, name)

	fmt.Printf("\nAssets to download into %s:\n", destDir)
	for i, a := range result.Assets {
		fmt.Printf("  %d. %s\n", i+1, a.Name)
	}

	if len(result.Assets) > 3 {
		fmt.Printf("\nNote: %d assets matched. Edit asset_patterns in config to narrow this down.\n",
			len(result.Assets))
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\nProceed with download? [y/N]: ")
	line, _ := reader.ReadString('\n')
	if !strings.EqualFold(strings.TrimSpace(line), "y") {
		fmt.Println("Cancelled.")
		return nil
	}

	var downloaded []string
	for _, asset := range result.Assets {
		path, err := downloadAsset(asset, destDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error downloading %s: %v\n", asset.Name, err)
			continue
		}
		downloaded = append(downloaded, path)
	}

	if len(downloaded) == 0 {
		return fmt.Errorf("all downloads failed")
	}

	fmt.Print("\nRun/open downloaded file(s)? [Y/n]: ")
	line, _ = reader.ReadString('\n')
	if !strings.EqualFold(strings.TrimSpace(line), "n") {
		for _, path := range downloaded {
			if err := runOrOpen(path); err != nil {
				fmt.Fprintf(os.Stderr, "  Error opening %s: %v\n", filepath.Base(path), err)
			}
		}
	}

	if app.Local.Type == "" || app.Local.Type == "none" {
		fmt.Print("\nMark version as installed/seen? [Y/n]: ")
		line, _ = reader.ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(line), "n") {
			state, err := LoadState()
			if err == nil {
				st := state.Get(name)
				st.LastSeenVersion = result.Version
				st.LatestVersion = result.Version
				_ = SaveState(state)
				fmt.Printf("Marked %q as version %s\n", name, result.Version)
			}
		}
	}

	return nil
}
