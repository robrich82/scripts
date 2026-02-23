package main

import "time"

// FileTimer abstracts filesystem timestamp operations so they can be replaced
// in tests without touching real files.
type FileTimer interface {
	// SetTimes sets the access and modification timestamps of a file.
	SetTimes(path string, t time.Time) error
	// SetCreationTime sets the file creation timestamp. On non-Windows platforms
	// this is a no-op because the concept does not exist.
	SetCreationTime(path string, t time.Time) error
}
