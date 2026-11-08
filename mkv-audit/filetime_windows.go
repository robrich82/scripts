//go:build windows

package main

import (
	"os"
	"syscall"
)

// copyCreationTime gives path the creation time recorded in src.
func copyCreationTime(path string, src os.FileInfo) error {
	attr, ok := src.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return nil
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(p, syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	err = syscall.SetFileTime(h, &attr.CreationTime, nil, nil)
	if cerr := syscall.CloseHandle(h); err == nil {
		err = cerr
	}
	return err
}
