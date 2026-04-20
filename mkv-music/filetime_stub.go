//go:build !windows

package main

import (
	"os"
	"time"
)

type win32FileTimer struct{}

func (w *win32FileTimer) CreationTimeNeedsUpdate(_ string, _ int64) bool {
	return false
}

func (w *win32FileTimer) SetTimes(path string, t time.Time) error {
	return os.Chtimes(path, t, t)
}

func (w *win32FileTimer) SetCreationTime(_ string, _ time.Time) error {
	return nil
}
