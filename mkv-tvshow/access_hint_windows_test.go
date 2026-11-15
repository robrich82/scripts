//go:build windows

package main

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAccessHint(t *testing.T) {
	rename := func(errno syscall.Errno) error {
		return &os.LinkError{Op: "rename", Old: ".tmp-1", New: "cache.json", Err: errno}
	}
	for _, tc := range []struct {
		name  string
		err   error
		write bool
		want  string // "" → no hint
	}{
		{"sharing violation", rename(windows.ERROR_SHARING_VIOLATION), true, "Another program has it open"},
		{"lock violation", rename(windows.ERROR_LOCK_VIOLATION), true, "Another program has it open"},
		{"save denied", rename(windows.ERROR_ACCESS_DENIED), true, "read-only, another program"},
		{"save denied names the folder", rename(windows.ERROR_ACCESS_DENIED), true, "permission to the file or its folder"},
		{"load denied", &os.PathError{Op: "open", Path: "cache.json", Err: windows.ERROR_ACCESS_DENIED}, false,
			"you don't have permission to read the file"},
		{"not found", &os.PathError{Op: "open", Path: "cache.json", Err: syscall.ENOENT}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := accessHint(tc.err, tc.write)
			if (tc.want == "" && got != "") || !strings.Contains(got, tc.want) {
				t.Errorf("accessHint = %q; want %q", got, tc.want)
			}
			if !tc.write && strings.Contains(got, "read-only") {
				t.Errorf("load hint blames read-only: %q", got)
			}
		})
	}
}
