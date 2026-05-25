package main

import (
	"fmt"
	"sort"
)

func cmdClean(_ []string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	state, err := LoadState()
	if err != nil {
		return err
	}

	known := make(map[string]bool, len(cfg.Apps))
	for _, a := range cfg.Apps {
		known[a.Name] = true
	}

	var removed []string
	for name := range state {
		if !known[name] {
			delete(state, name)
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)

	if len(removed) == 0 {
		fmt.Println("State is clean; nothing to remove.")
		return nil
	}

	for _, name := range removed {
		fmt.Printf("Removed stale state for %q\n", name)
	}
	n := len(removed)
	suffix := "ies"
	if n == 1 {
		suffix = "y"
	}
	fmt.Printf("\nRemoved %d stale state entr%s.\n", n, suffix)
	return SaveState(state)
}
