//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	shell32       = syscall.NewLazyDLL("shell32.dll")
	shellExecuteW = shell32.NewProc("ShellExecuteW")
)

// runElevated launches file with a UAC elevation prompt via ShellExecuteW "runas".
func runElevated(file, params string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	fp, _ := syscall.UTF16PtrFromString(file)
	var pp *uint16
	if params != "" {
		pp, _ = syscall.UTF16PtrFromString(params)
	}
	ret, _, _ := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(fp)),
		uintptr(unsafe.Pointer(pp)),
		0,
		1, // SW_SHOWNORMAL
	)
	if ret <= 32 {
		return fmt.Errorf("ShellExecuteW returned %d", ret)
	}
	return nil
}
