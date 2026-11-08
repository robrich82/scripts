package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runClean() {
	cfg, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	changed := false
	for i := range cfg.Libraries {
		lib := &cfg.Libraries[i]
		if _, err := os.Stat(lib.Path); os.IsNotExist(err) {
			if pathGone(lib.Path) {
				fmt.Printf("Warning: library %q root path does not exist: %s\n", lib.Name, lib.Path)
			} else {
				fmt.Printf("Warning: library %q is offline, keeping its overrides: %s\n", lib.Name, lib.Path)
			}
		}

		var kept []DirOverride
		for _, override := range lib.Overrides {
			full := filepath.Join(lib.Path, override.Path)
			if pathGone(full) {
				fmt.Printf("Removing stale override %q in library %q (path not found)\n", override.Path, lib.Name)
				changed = true
			} else {
				kept = append(kept, override)
			}
		}
		lib.Overrides = kept
	}

	if !changed {
		fmt.Println("Config is clean; nothing to remove.")
		return
	}

	if err := saveGlobalConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Config saved.")
}
