package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

var stdinReader = bufio.NewReader(os.Stdin)

// ask prints msg and reads a trimmed line from stdin.
func ask(msg string) string {
	fmt.Print(msg)
	line, _ := stdinReader.ReadString('\n')
	return strings.TrimSpace(line)
}

// confirm returns true if the user answers y/yes (default yes if blank).
func confirm(msg string) bool {
	ans := ask(msg + " [Y/n]: ")
	return ans == "" || strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes")
}

// confirmNo returns true only on explicit y/yes (default no).
func confirmNo(msg string) bool {
	ans := ask(msg + " [y/N]: ")
	return strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes")
}

// matchMangaUpdates searches MangaUpdates by title and walks the user through
// confirming the right series, mirroring the TMDB flow in mkv-tvshow.
// Returns the confirmed muSeries, or nil if skipped.
func matchMangaUpdates(mu *MUClient, title string) *muSeries {
	results, err := mu.SearchSeries(title)
	if err != nil || len(results) == 0 {
		fmt.Printf("  No MangaUpdates results for %q.\n", title)
		idStr := ask("  Enter MangaUpdates numeric ID, or press Enter to skip: ")
		if idStr == "" {
			return nil
		}
		var id int64
		fmt.Sscanf(idStr, "%d", &id)
		if id == 0 {
			return nil
		}
		s, err := mu.GetSeries(id)
		if err != nil {
			fmt.Printf("  [!] Could not fetch ID %d: %v\n", id, err)
			return nil
		}
		return s
	}

	best := results[0]
	fmt.Printf("  Match: %s (%s, %s)\n", best.Title, best.Type, best.Year)
	if best.URL != "" {
		fmt.Printf("  Link:  %s\n", best.URL)
	}
	ans := ask("  Correct? [y/n] or enter MangaUpdates ID: ")
	ans = strings.TrimSpace(ans)
	switch strings.ToLower(ans) {
	case "y", "":
		return &best
	case "n", "s":
		return nil
	default:
		// Treat as a manual ID.
		var id int64
		fmt.Sscanf(ans, "%d", &id)
		if id == 0 {
			return nil
		}
		s, err := mu.GetSeries(id)
		if err != nil {
			fmt.Printf("  [!] Could not fetch ID %d: %v\n", id, err)
			return nil
		}
		return s
	}
}
