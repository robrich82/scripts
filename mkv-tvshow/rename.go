package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// otherFileAt reports whether a file other than from already exists at to.
// Only a case-only rename may find from itself there (on a case-insensitive
// filesystem): os.SameFile alone would pass a hard link to from, or a file on
// a network drive that reports made-up file IDs.
func otherFileAt(from, to string) (bool, error) {
	toInfo, err := os.Lstat(to)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(filepath.Base(from), filepath.Base(to)) {
		return true, nil
	}
	fromInfo, err := os.Lstat(from)
	if err != nil {
		return false, err
	}
	return !os.SameFile(fromInfo, toInfo), nil
}

// renameNoReplace is os.Rename, except that it fails rather than replace
// another file already at to.
func renameNoReplace(from, to string) error {
	taken, err := otherFileAt(from, to)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%s already exists", filepath.Base(to))
	}
	return os.Rename(from, to)
}
