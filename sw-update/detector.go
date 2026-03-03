package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// LocalDetector detects the locally installed version of an app.
type LocalDetector interface {
	InstalledVersion(app App) (string, error)
}

// NewDetector returns a LocalDetector that dispatches based on app.Local.Type.
func NewDetector() LocalDetector {
	return &detectorDispatch{}
}

type detectorDispatch struct{}

func (d *detectorDispatch) InstalledVersion(app App) (string, error) {
	switch app.Local.Type {
	case "", "none":
		return "", nil
	case "exe":
		return detectViaExe(app.Local)
	case "file-version":
		if app.Local.Path == "" {
			return "", fmt.Errorf("file-version detector requires path")
		}
		return detectFileVersion(app.Local.Path)
	default:
		return "", fmt.Errorf("unknown local detector type: %s", app.Local.Type)
	}
}

func detectViaExe(cfg LocalConfig) (string, error) {
	if cfg.Path == "" {
		return "", fmt.Errorf("exe detector requires path")
	}
	if cfg.VersionRegex == "" {
		return "", fmt.Errorf("exe detector requires version_regex")
	}
	cmd := exec.Command(cfg.Path, cfg.Args...)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("exec %s: %w", cfg.Path, err)
	}
	re, err := regexp.Compile(cfg.VersionRegex)
	if err != nil {
		return "", fmt.Errorf("invalid version_regex: %w", err)
	}
	m := re.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("version_regex did not match output of %s", cfg.Path)
	}
	if len(m) > 1 {
		return strings.TrimSpace(string(m[1])), nil
	}
	return strings.TrimSpace(string(m[0])), nil
}
