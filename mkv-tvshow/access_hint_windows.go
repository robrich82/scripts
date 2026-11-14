//go:build windows

package main

import (
	"errors"
	"io/fs"

	"golang.org/x/sys/windows"
)

// accessHint says what to do about err from reading (write false) or saving
// (write true) a cache file, or returns "" when err is not an access problem.
func accessHint(err error, write bool) string {
	switch {
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION), errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return "Another program has it open; close that program, then try again."
	case errors.Is(err, fs.ErrPermission) && write:
		// Saving writes a temp file in the folder and renames it over the
		// cache. Renaming over a file another program holds open is also
		// refused as access denied, so this can't tell the causes apart.
		return "Access was denied: the file is read-only, another program (an editor, antivirus) has it open, " +
			"or you don't have permission to the file or its folder."
	case errors.Is(err, fs.ErrPermission):
		// A read-only file still reads, and a file another program holds
		// open is a sharing violation.
		return "Access was denied: you don't have permission to read the file."
	}
	return ""
}
