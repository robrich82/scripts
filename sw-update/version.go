package main

import (
	"strconv"
	"strings"
)

// CompareVersions compares two version strings.
// Returns 1 if a > b, 0 if equal, -1 if a < b.
// Strips leading 'v', compares numeric dot-separated segments,
// then treats pre-release suffix (after '-') as less than no suffix.
func CompareVersions(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")

	// Split pre-release suffix on first '-'
	aParts := strings.SplitN(a, "-", 2)
	bParts := strings.SplitN(b, "-", 2)

	aSegs := strings.Split(aParts[0], ".")
	bSegs := strings.Split(bParts[0], ".")

	maxLen := len(aSegs)
	if len(bSegs) > maxLen {
		maxLen = len(bSegs)
	}

	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(aSegs) {
			av, _ = strconv.Atoi(aSegs[i])
		}
		if i < len(bSegs) {
			bv, _ = strconv.Atoi(bSegs[i])
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}

	// Pre-release suffix: no suffix > has suffix (e.g. "1.0" > "1.0-beta")
	aHasPre := len(aParts) > 1
	bHasPre := len(bParts) > 1
	if !aHasPre && bHasPre {
		return 1
	}
	if aHasPre && !bHasPre {
		return -1
	}
	if aHasPre && bHasPre {
		if aParts[1] > bParts[1] {
			return 1
		}
		if aParts[1] < bParts[1] {
			return -1
		}
	}

	return 0
}
