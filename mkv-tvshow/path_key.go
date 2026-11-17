package main

import (
	"runtime"
	"strings"
)

// foldPathCase is whether the retry list's and title cache's paths compare
// case-insensitively, as they do on Windows; tests set it.
var foldPathCase = runtime.GOOS == "windows"

// pathKey is p in the form those caches' lookups compare.
func pathKey(p string) string {
	if foldPathCase {
		return strings.ToLower(p)
	}
	return p
}
