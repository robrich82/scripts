//go:build windows

package main

import (
	"errors"
	"io/fs"

	"golang.org/x/sys/windows"
)

// accessHint says what to do about err from reading or writing a cache file,
// or returns "" when err is not an access problem.
func accessHint(err error) string {
	switch {
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION), errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return "Another program has it open; close that program, then try again."
	case errors.Is(err, fs.ErrPermission):
		// Renaming over a file another program holds open is also refused as
		// access denied, so this can't tell the two apart.
		return "Access was denied: the file is read-only, or another program (an editor, antivirus) has it open."
	}
	return ""
}
