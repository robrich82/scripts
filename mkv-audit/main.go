package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	libraryFlag := flag.String("library", "", "library name to use (overrides auto-detection)")
	flag.Parse()

	runner, err := newRealMKVRunner()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
		os.Exit(1)
	}

	cfg, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}

	var lib *Library
	if *libraryFlag != "" {
		for i := range cfg.Libraries {
			if cfg.Libraries[i].Name == *libraryFlag {
				l := cfg.Libraries[i]
				lib = &l
				break
			}
		}
		if lib == nil {
			fmt.Fprintf(os.Stderr, "Library %q not found in config\n", *libraryFlag)
			os.Exit(1)
		}
	} else {
		lib = detectLibrary(dir, cfg)
	}

	var stateDir string
	if lib != nil {
		fmt.Printf("--- Library: %s ---\n", lib.Name)
		stateDir = filepath.Join(cfgDir, lib.Name)
	} else {
		if len(cfg.Libraries) > 0 {
			fmt.Fprintf(os.Stderr, "Warning: no library matched current directory; running without overrides\n")
		}
		stateDir = cfgDir
	}

	// Wire up TMDB client when an API key is available. The library type
	// ("tv" or "movie") controls which TMDB search endpoint is used.
	var tmdb TMDBOriginClient
	if apiKey := strings.TrimSpace(os.Getenv("TMDB_API_KEY")); apiKey != "" {
		if lib != nil && (lib.Type == "tv" || lib.Type == "movie") {
			tmdb = newHTTPTMDBOriginClient(apiKey)
			fmt.Printf("--- TMDB origin lookup: enabled (%s) ---\n", lib.Type)
		}
	}

	fmt.Println("--- Running Library Architect ---")

	stats := RunAll(dir, stateDir, lib, runner, runner, newStdinPrompter(), tmdb)

	fmt.Println()
	fmt.Println("========================================")
	fmt.Printf("%-24s%s\n", "FINAL SUMMARY", "")
	fmt.Println("========================================")
	fmt.Printf("Total Scanned:          %d\n", stats.Scanned)
	fmt.Printf("Total Fixed:            %d\n", stats.Fixed)
	fmt.Printf("Total Failed/Skipped:   %d\n", stats.Failed)
	fmt.Println("----------------------------------------")
	fmt.Printf("Langs Normalized:       %d\n", stats.LangFixes)
	fmt.Printf("IETF Tags Fixed:        %d\n", stats.IETFFixes)
	fmt.Printf("Video Lang Inherited:   %d\n", stats.VideoInherit)
	fmt.Printf("Flag Rules Applied:     %d\n", stats.FlagFixes)
	fmt.Printf("Names Added:            %d\n", stats.NamesAdded)
	fmt.Println("========================================")
}
