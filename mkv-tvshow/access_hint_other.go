//go:build !windows

package main

import (
	"errors"
	"io/fs"
)

// accessHint says what to do about err from reading or writing a cache file,
// or returns "" when err is not an access problem.
func accessHint(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "Permission denied: check the permissions on the file and its folder."
	}
	return ""
}
