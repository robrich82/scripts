package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	check := flag.Bool("check", false, "list files that need processing and exit 1 if any found")
	dryRun := flag.Bool("dry-run", false, "preview changes without modifying files")
	verbose := flag.Bool("verbose", false, "print each file rename")
	noBlank := flag.Bool("no-blank", false, "skip blank page insertion")
	backup := flag.Bool("backup", false, "create a .bak file before overwriting")
	flag.Parse()

	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}

	// --check implies dry-run
	impliedDryRun := *dryRun || *check

	var needsFix, errCount int
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if isHiddenDir(d.Name(), path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".cbz") {
			return nil
		}

		if !*check {
			fmt.Printf("%s\n", path)
		}

		skipped, processErr := processCBZ(path, impliedDryRun, *verbose, *noBlank, !*backup)
		if processErr != nil {
			if *check {
				fmt.Printf("%s\n", path)
			}
			fmt.Fprintf(os.Stderr, "  ERROR: %v\n", processErr)
			errCount++
		} else if !skipped {
			needsFix++
			if *check {
				fmt.Printf("%s\n", path)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk error: %v\n", err)
		os.Exit(1)
	}

	switch {
	case *check:
		fmt.Printf("\nDone — needs fixing: %d  errors: %d\n", needsFix, errCount)
		if needsFix > 0 || errCount > 0 {
			os.Exit(1)
		}
	case *dryRun:
		fmt.Printf("\nDone — would process: %d  errors: %d\n", needsFix, errCount)
	default:
		fmt.Printf("\nDone — processed: %d  errors: %d\n", needsFix, errCount)
	}
}
