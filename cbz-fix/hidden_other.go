//go:build !windows

package main

import "strings"

func isHiddenDir(name, path string) bool {
	// Dot-prefixed directories are hidden, but never the walk roots "." or "..".
	return name != "." && name != ".." && strings.HasPrefix(name, ".")
}
