//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// detectFileVersion reads the Windows PE file version via PowerShell.
func detectFileVersion(path string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("(Get-Item '%s').VersionInfo.FileVersion", path))
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
