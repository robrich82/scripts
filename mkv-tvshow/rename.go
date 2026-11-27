package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// errNameTaken is renameNoReplace's error when another file has the new name.
var errNameTaken = errors.New("already exists")

// otherFileAt reports whether to names an existing file other than from. A
// case-only rename on a case-insensitive filesystem finds from itself at to.
func otherFileAt(from, to string) (bool, error) {
	toInfo, err := os.Lstat(to)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fromInfo, err := os.Lstat(from)
	if err != nil {
		return false, err
	}
	return !os.SameFile(fromInfo, toInfo), nil
}

// renameNoReplace renames from to to, refusing when another file already has
// that name: os.Rename would replace it.
func renameNoReplace(from, to string) error {
	taken, err := otherFileAt(from, to)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%s %w", filepath.Base(to), errNameTaken)
	}
	return os.Rename(from, to)
}
