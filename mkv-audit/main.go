package main

import (
	"fmt"
	"os"
)

func main() {
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

	fmt.Println("--- Running Library Architect ---")

	stats := RunAll(dir, runner, runner, newStdinPrompter())

	fmt.Println()
	fmt.Println("========================================")
	fmt.Printf("%-24s%s\n", "FINAL SUMMARY", "")
	fmt.Println("========================================")
	fmt.Printf("Total Scanned:          %d\n", stats.Scanned)
	fmt.Printf("Total Fixed:            %d\n", stats.Fixed)
	fmt.Printf("Total Failed/Skipped:   %d\n", stats.Failed)
	fmt.Println("----------------------------------------")
	fmt.Printf("Langs Normalized:       %d\n", stats.LangFixes)
	fmt.Printf("Flag Rules Applied:     %d\n", stats.FlagFixes)
	fmt.Println("========================================")
}
