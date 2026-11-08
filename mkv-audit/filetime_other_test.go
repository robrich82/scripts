//go:build !windows

package main

import (
	"os"
	"time"
)

// creationTime returns the zero time: there is no creation time to compare.
func creationTime(os.FileInfo) time.Time { return time.Time{} }
