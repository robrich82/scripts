//go:build !windows

package main

import "fmt"

// detectFileVersion is not implemented on non-Windows platforms.
func detectFileVersion(path, field string) (string, error) {
	return "", fmt.Errorf("file-version detection not supported on this platform")
}
