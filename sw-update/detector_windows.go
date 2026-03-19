//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// detectFileVersion reads a Windows PE version field via PowerShell.
// field selects which VersionInfo property to read; defaults to "FileVersion".
func detectFileVersion(path, field string) (string, error) {
	if field == "" {
		field = "FileVersion"
	}
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("(Get-Item '%s').VersionInfo.%s", path, field))
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("detect file version of %s: %w", path, err)
	}
	ver := strings.TrimSpace(string(out))
	if ver == "" {
		return "", fmt.Errorf("no version info for %s", path)
	}
	return ver, nil
}
