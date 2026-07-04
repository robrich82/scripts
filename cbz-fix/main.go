package main

import (
	"bufio"
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
	updateCover := flag.Bool("update-cover", false, "replace cover.* in each CBZ with a larger cover.jpg from the same directory")
	flag.Parse()

	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}

	// --check implies dry-run
	impliedDryRun := *dryRun || *check

	// Build a prompt function once; used only in --update-cover mode.
	stdinReader := bufio.NewReader(os.Stdin)
	prompt := func(q string) (bool, error) {
		fmt.Print(q)
		line, err := stdinReader.ReadString('\n')
		if err != nil {
			return false, err
		}
		s := strings.ToLower(strings.TrimSpace(line))
		return s == "y" || s == "yes", nil
	}

	var needsFix, renamed, errCount int
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

		// A .cbz that is really a RAR/7z/ACE/tar archive can't be processed as
		// a zip. Rename it to the correct comic extension and move on.
		mis, sniffErr := sniffArchive(path)
		if sniffErr != nil {
			fmt.Printf("%s\n", path)
			fmt.Fprintf(os.Stderr, "  ERROR: %v\n", sniffErr)
			errCount++
			return nil
		}
		if mis != nil {
			fmt.Printf("%s\n", path)
			newPath := strings.TrimSuffix(path, filepath.Ext(path)) + mis.correctExt
			if impliedDryRun {
				fmt.Printf("  is a %s archive, not a zip; would rename to %s (not processed)\n", mis.format, filepath.Base(newPath))
				renamed++
				return nil
			}
			if _, statErr := os.Stat(newPath); statErr == nil {
				fmt.Fprintf(os.Stderr, "  ERROR: is a %s archive but %s already exists; leaving as-is\n", mis.format, filepath.Base(newPath))
				errCount++
				return nil
			}
			if renErr := os.Rename(path, newPath); renErr != nil {
				fmt.Fprintf(os.Stderr, "  ERROR: rename to %s: %v\n", filepath.Base(newPath), renErr)
				errCount++
				return nil
			}
			fmt.Printf("  is a %s archive, not a zip; renamed to %s (not processed)\n", mis.format, filepath.Base(newPath))
			renamed++
			return nil
		}

		if !*check {
			fmt.Printf("%s\n", path)
		}

		var skipped bool
		var processErr error
		if *updateCover {
			skipped, processErr = updateCoverCBZ(path, impliedDryRun, *verbose, !*backup, prompt)
		} else {
			skipped, processErr = processCBZ(path, impliedDryRun, *verbose, *noBlank, !*backup)
		}
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
		fmt.Printf("\nDone — needs fixing: %d  renamed: %d  errors: %d\n", needsFix, renamed, errCount)
		if needsFix > 0 || renamed > 0 || errCount > 0 {
			os.Exit(1)
		}
	case *dryRun:
		fmt.Printf("\nDone — would process: %d  would rename: %d  errors: %d\n", needsFix, renamed, errCount)
	default:
		fmt.Printf("\nDone — processed: %d  renamed: %d  errors: %d\n", needsFix, renamed, errCount)
	}
}
