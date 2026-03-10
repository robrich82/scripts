package main

import (
	"fmt"
	"strings"
	"time"
)

func cmdList(_ []string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	state, err := LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if len(cfg.Apps) == 0 {
		fmt.Println("No apps configured.")
		fmt.Println("Run: sw-update import <bookmarks.html>")
		return nil
	}

	fmt.Printf("%-35s  %-7s  %-10s  %-10s  %-10s  %s\n",
		"Name", "Enabled", "Installed", "Seen", "Latest", "Last Checked")
	fmt.Printf("%-35s  %-7s  %-10s  %-10s  %-10s  %s\n",
		strings.Repeat("-", 35), "-------", "----------", "----------", "----------", "------------")

	for _, app := range cfg.Apps {
		st := state.Get(app.Name)
		enabledStr := "yes"
		if !app.Enabled {
			enabledStr = "no"
		}
		lastChecked := ""
		if !st.LastChecked.IsZero() {
			lastChecked = st.LastChecked.Format(time.DateOnly)
		}
		fmt.Printf("%-35s  %-7s  %-10s  %-10s  %-10s  %s\n",
			truncate(app.Name, 35),
			enabledStr,
			truncate(st.InstalledVersion, 10),
			truncate(st.LastSeenVersion, 10),
			truncate(st.LatestVersion, 10),
			lastChecked,
		)
	}

	enabled := 0
	for _, a := range cfg.Apps {
		if a.Enabled {
			enabled++
		}
	}
	fmt.Printf("\nTotal: %d apps (%d enabled)\n", len(cfg.Apps), enabled)
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "~"
}
