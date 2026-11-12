package main

import "time"

// FileTimer abstracts filesystem timestamp operations for testability.
type FileTimer interface {
	SetTimes(path string, t time.Time) error
	SetCreationTime(path string, t time.Time) error
}
