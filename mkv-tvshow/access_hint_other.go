//go:build !windows

package main

import (
	"errors"
	"io/fs"
)

// accessHint says what to do about err from reading (write false) or saving
// (write true) a cache file, or returns "" when err is not an access problem.
func accessHint(err error, write bool) string {
	switch {
	case errors.Is(err, fs.ErrPermission) && write:
		// Saving writes a temp file in the folder and renames it over the cache.
		return "Permission denied: check the permissions on the file and its folder."
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied: check the permissions on the file."
	}
	return ""
}
