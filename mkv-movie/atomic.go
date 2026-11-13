package main

import (
	"os"
	"path/filepath"
)

// atomicWriteFile writes data (mode 0644) by creating a temp file in the same
// directory and renaming it over path. An interrupted or failed write therefore
// never leaves a truncated/corrupt cache file in place — the previous contents
// survive intact.
func atomicWriteFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Flush to disk before the rename, so a power loss can't leave the new
	// name pointing at unwritten (zero-filled) data.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
