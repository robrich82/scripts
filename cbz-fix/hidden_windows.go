//go:build windows

package main

import (
	"strings"
	"syscall"
)

func isHiddenDir(name, path string) bool {
	// Treat dot-prefixed directories (.git, .stfolder, …) as hidden, but never
	// the walk roots "." or "..", or an entire run started from "." is skipped.
	if name != "." && name != ".." && strings.HasPrefix(name, ".") {
		return true
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return false
	}
	return attrs&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
