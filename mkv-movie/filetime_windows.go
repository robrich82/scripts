//go:build windows

package main

import (
	"math"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type win32FileTimer struct{}

func (w *win32FileTimer) CreationTimeNeedsUpdate(path string, targetUnix int64) bool {
	var data syscall.Win32FileAttributeData
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	if err := syscall.GetFileAttributesEx(p, syscall.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&data))); err != nil {
		return false
	}
	ctUnix := int64(data.CreationTime.Nanoseconds()) / 1e9
	return math.Abs(float64(ctUnix-targetUnix)) > 1
}

func (w *win32FileTimer) SetTimes(path string, t time.Time) error {
	return os.Chtimes(path, t, t)
}

func (w *win32FileTimer) SetCreationTime(path string, t time.Time) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return err
	}
	ft := windows.NsecToFiletime(t.UnixNano())
	err = windows.SetFileTime(handle, &ft, nil, nil)
	if cerr := windows.CloseHandle(handle); err == nil {
		err = cerr
	}
	return err
}
