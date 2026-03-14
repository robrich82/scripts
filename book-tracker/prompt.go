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
// preferType ("Manga", "Novel", "") causes matching-type results to be listed
// first. Returns the confirmed muSeries, or nil if skipped.
func matchMangaUpdates(mu *MUClient, title string, preferType string) *muSeries {
	results, err := mu.SearchSeries(title)
	if err != nil || len(results) == 0 {
		fmt.Printf("  No MangaUpdates results for %q.\n", title)
		ans := ask("  Enter MangaUpdates URL/ID, or press Enter to skip: ")
		return resolveManualInput(mu, ans)
	}

	// Sort: preferred type first, then others.
	sorted := sortByType(results, preferType)

	// Display all results numbered.
	fmt.Println("  Results:")
	for i, r := range sorted {
		fmt.Printf("    %d. %s (%s, %s)\n", i+1, r.Title, r.Type, r.Year)
		if r.URL != "" {
			fmt.Printf("       %s\n", r.URL)
		}
	}

	ans := ask("  Enter number, URL/ID, or press Enter to skip: ")
	ans = strings.TrimSpace(ans)
	if ans == "" || strings.EqualFold(ans, "s") {
		return nil
	}

	// Try as a list index first.
	var idx int
	if n, _ := fmt.Sscanf(ans, "%d", &idx); n == 1 && idx >= 1 && idx <= len(sorted) {
		return &sorted[idx-1]
	}

	// Otherwise treat as URL/ID.
	return resolveManualInput(mu, ans)
}

// sortByType returns results with preferType first, preserving relative order
// within each group.
func sortByType(results []muSeries, preferType string) []muSeries {
	if preferType == "" {
		return results
	}
	var preferred, rest []muSeries
	for _, r := range results {
		if strings.EqualFold(r.Type, preferType) {
			preferred = append(preferred, r)
		} else {
			rest = append(rest, r)
		}
	}
	return append(preferred, rest...)
}

// resolveManualInput resolves a user-entered string to a muSeries.
// Accepts:
//   - A numeric ID:              "54385586419"
//   - A MangaUpdates URL slug:   "ozfrv1v"  (base-36 encoded series ID)
//   - A full MangaUpdates URL:   "https://www.mangaupdates.com/series/ozfrv1v/date-a-live"
func resolveManualInput(mu *MUClient, input string) *muSeries {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	id := parseIDOrSlug(input)
	if id == 0 {
		fmt.Printf("  [!] Could not parse %q as an ID or URL.\n", input)
		return nil
	}

	s, err := mu.GetSeries(id)
	if err != nil {
		fmt.Printf("  [!] Could not fetch series %d: %v\n", id, err)
		return nil
	}
	return s
}

// parseIDOrSlug converts user input to a MangaUpdates series ID.
// MangaUpdates encodes numeric IDs as base-36 slugs in URLs.
func parseIDOrSlug(input string) int64 {
	// Extract slug from a full URL if needed.
	// e.g. "https://www.mangaupdates.com/series/ozfrv1v/date-a-live" → "ozfrv1v"
	slug := input
	if strings.Contains(input, "/series/") {
		parts := strings.Split(input, "/series/")
		if len(parts) > 1 {
			// Take the first path segment after /series/
			slug = strings.SplitN(parts[1], "/", 2)[0]
		}
	}

	// Try plain decimal first (a pure numeric entry).
	var decID int64
	if n, _ := fmt.Sscanf(slug, "%d", &decID); n == 1 && fmt.Sprintf("%d", decID) == slug {
		return decID
	}

	// Decode as base-36 (MangaUpdates URL slug format).
	return base36Decode(slug)
}

// base36Decode decodes a lowercase base-36 string to int64.
func base36Decode(s string) int64 {
	var id int64
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= '0' && c <= '9':
			id = id*36 + int64(c-'0')
		case c >= 'a' && c <= 'z':
			id = id*36 + int64(c-'a') + 10
		default:
			return 0 // not a valid base-36 string
		}
	}
	return id
}
