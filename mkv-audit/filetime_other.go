//go:build !windows

package main

import "os"

// copyCreationTime is a no-op: other platforms have no settable creation time.
func copyCreationTime(string, os.FileInfo) error { return nil }
