package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "preview changes without modifying files")
	verbose := flag.Bool("verbose", false, "print each file rename")
	flag.Parse()

	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}

	var processed, errCount int
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".cbz") {
			return nil
		}

		fmt.Printf("%s\n", path)
		if processErr := processCBZ(path, *dryRun, *verbose); processErr != nil {
			fmt.Fprintf(os.Stderr, "  ERROR: %v\n", processErr)
			errCount++
		} else {
			processed++
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk error: %v\n", err)
		os.Exit(1)
	}

	label := "processed"
	if *dryRun {
		label = "would process"
	}
	fmt.Printf("\nDone — %s: %d  errors: %d\n", label, processed, errCount)
}
