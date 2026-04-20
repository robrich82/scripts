package main

import "time"

// FileTimer abstracts filesystem timestamp operations for testability.
type FileTimer interface {
	// CreationTimeNeedsUpdate returns true if the file's creation timestamp
	// differs from targetUnix by more than one second.
	CreationTimeNeedsUpdate(path string, targetUnix int64) bool
	SetTimes(path string, t time.Time) error
	SetCreationTime(path string, t time.Time) error
}
