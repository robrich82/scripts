package main

import "strings"

// sequenceRatio computes a string similarity ratio in [0, 1] using the
// longest common subsequence approximation of Python's difflib.SequenceMatcher:
//
//	ratio = 2 * LCS_length / (len(a) + len(b))
//
// Comparison is case-sensitive to match Python difflib behaviour.
func sequenceRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1.0
	}
	if len(ra) == 0 || len(rb) == 0 {
		return 0.0
	}
	if a == b {
		return 1.0
	}
	lcs := lcsLength(ra, rb)
	return 2.0 * float64(lcs) / float64(len(ra)+len(rb))
}

// lcsLength computes the length of the longest common subsequence of a and b
// using a two-row dynamic programming table.
func lcsLength(a, b []rune) int {
	m, n := len(a), len(b)
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1] + 1
			} else if prev[j] > curr[j-1] {
				curr[j] = prev[j]
			} else {
				curr[j] = curr[j-1]
			}
		}
		prev, curr = curr, make([]int, n+1)
	}
	return prev[n]
}

// getCloseMatch returns the best match for query among choices whose ratio
// meets or exceeds cutoff. Returns ("", false) if no match qualifies.
func getCloseMatch(query string, choices []string, cutoff float64) (string, bool) {
	best := ""
	bestRatio := -1.0
	for _, c := range choices {
		r := sequenceRatio(query, c)
		if r > bestRatio {
			bestRatio = r
			best = c
		}
	}
	if bestRatio >= cutoff {
		return best, true
	}
	return "", false
}

// removeIgnoredParts strips each pattern from filename and collapses extra spaces.
func removeIgnoredParts(filename string, patterns []string) string {
	result := filename
	for _, p := range patterns {
		result = strings.ReplaceAll(result, p, "")
	}
	// Collapse runs of spaces.
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return strings.TrimSpace(result)
}
