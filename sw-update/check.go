package main

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"
)

type checkEntry struct {
	app       App
	state     *AppState
	result    *CheckResult
	err       error
	detectErr error
	hasUpdate bool
}

func cmdCheck(args []string) error {
	verbose := false
	var filtered []string
	for _, a := range args {
		if a == "-v" || a == "--verbose" {
			verbose = true
		} else {
			filtered = append(filtered, a)
		}
	}
	args = filtered

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	state, err := LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	apps := cfg.Apps

	// Filter to a specific app if named.
	if len(args) > 0 {
		name := args[0]
		var found *App
		for i := range apps {
			if apps[i].Name == name {
				found = &apps[i]
				break
			}
		}
		if found == nil {
			return fmt.Errorf("app %q not found", name)
		}
		apps = []App{*found}
	} else {
		// Only check enabled apps when running all.
		var enabled []App
		for _, a := range apps {
			if a.Enabled {
				enabled = append(enabled, a)
			}
		}
		apps = enabled
	}

	if len(apps) == 0 {
		fmt.Println("No enabled apps to check.")
		return nil
	}

	client := &http.Client{Timeout: 30 * time.Second, Transport: checkTransport}
	checker := NewChecker(client)
	detector := NewDetector()

	// Pre-populate state entries so goroutines each have their own pointer.
	entries := make([]checkEntry, len(apps))
	for i, a := range apps {
		entries[i].app = a
		entries[i].state = state.Get(a.Name)
	}

	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup

	fmt.Printf("Checking %d app(s)...\n", len(apps))

	for i := range entries {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			e := &entries[idx]
			result, checkErr := checker.Check(e.app)
			installed, detectErr := detector.InstalledVersion(e.app)

			e.result = result
			e.err = checkErr
			e.detectErr = detectErr
			switch {
			case e.app.Local.Type == "" || e.app.Local.Type == "none":
				// No local detector: clear any stale installed_version so a
				// prior detection (or an earlier detector config) can't shadow
				// last_seen_version in KnownVersion.
				e.state.InstalledVersion = ""
			case installed != "":
				e.state.InstalledVersion = installed
			}
			if result != nil {
				e.state.LatestVersion = result.Version
				e.state.LastChecked = time.Now()
				known := e.state.KnownVersion()
				switch {
				case known == "":
					e.hasUpdate = true
				case e.app.Checker.VersionCompare == "hash":
					e.hasUpdate = !HashPrefixMatch(result.Version, known)
				default:
					e.hasUpdate = CompareVersions(result.Version, known) > 0
				}
			}
		}(i)
	}

	wg.Wait()

	if err := SaveState(state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save state: %v\n", err)
	}

	var updates, upToDate, errors []checkEntry
	for _, e := range entries {
		switch {
		case e.err != nil:
			errors = append(errors, e)
		case e.hasUpdate:
			updates = append(updates, e)
		default:
			upToDate = append(upToDate, e)
		}
	}

	nameWidth := 0
	for _, e := range entries {
		if len(e.app.Name) > nameWidth {
			nameWidth = len(e.app.Name)
		}
	}

	if len(updates) > 0 {
		fmt.Printf("\n=== UPDATES AVAILABLE (%d) ===\n", len(updates))
		for _, e := range updates {
			known := e.state.KnownVersion()
			if known == "" {
				fmt.Printf("  [NEW]  %-*s  %s\n", nameWidth, e.app.Name, e.result.Version)
			} else {
				fmt.Printf("  [UPD]  %-*s  %s -> %s\n", nameWidth, e.app.Name, known, e.result.Version)
			}
			for _, a := range e.result.Assets {
				fmt.Printf("           %s\n", a.Name)
			}
		}
	}

	if verbose && len(upToDate) > 0 {
		fmt.Printf("\n=== UP TO DATE (%d) ===\n", len(upToDate))
		for _, e := range upToDate {
			fmt.Printf("  [OK]   %-*s  %s\n", nameWidth, e.app.Name, e.result.Version)
		}
	}

	if len(errors) > 0 {
		fmt.Printf("\n=== ERRORS (%d) ===\n", len(errors))
		for _, e := range errors {
			fmt.Printf("  [ERR]  %-*s  %v\n", nameWidth, e.app.Name, e.err)
		}
	}

	// Detector warnings are reported separately from checker errors: the remote
	// check may have succeeded while local-version detection failed (stale path,
	// changed --version output, etc.). Without this, a broken detector would
	// silently leave installed_version untouched and updates could go unnoticed.
	var detectWarnings []checkEntry
	for _, e := range entries {
		if e.detectErr != nil {
			detectWarnings = append(detectWarnings, e)
		}
	}
	if len(detectWarnings) > 0 {
		fmt.Printf("\n=== DETECTOR WARNINGS (%d) ===\n", len(detectWarnings))
		for _, e := range detectWarnings {
			fmt.Printf("  [WARN] %-*s  %v\n", nameWidth, e.app.Name, e.detectErr)
		}
	}

	return nil
}
