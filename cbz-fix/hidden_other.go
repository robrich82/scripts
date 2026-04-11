//go:build !windows

package main

import "strings"

func isHiddenDir(name, path string) bool {
	return strings.HasPrefix(name, ".")
}
