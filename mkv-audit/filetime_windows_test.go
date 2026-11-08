//go:build windows

package main

import (
	"os"
	"syscall"
	"time"
)

func creationTime(st os.FileInfo) time.Time {
	return time.Unix(0, st.Sys().(*syscall.Win32FileAttributeData).CreationTime.Nanoseconds())
}
