package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
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
	var ver string
	var err error
	switch app.Local.Type {
	case "", "none":
		return "", nil
	case "exe":
		ver, err = detectViaExe(app.Local)
	case "file-version":
		if app.Local.Path == "" {
			return "", fmt.Errorf("file-version detector requires path")
		}
		ver, err = detectFileVersion(app.Local.Path, app.Local.VersionField)
		if err == nil && app.Local.VersionRegex != "" {
			ver, err = applyVersionRegex(ver, app.Local.VersionRegex)
		}
	case "filename":
		ver, err = detectViaFilename(app.Local)
	default:
		return "", fmt.Errorf("unknown local detector type: %s", app.Local.Type)
	}
	if err != nil {
		return "", err
	}
	return applyVersionRewrite(ver, app.Local.VersionRewrite)
}

func applyVersionRegex(ver, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid version_regex: %w", err)
	}
	m := re.FindStringSubmatch(ver)
	if m == nil {
		return "", fmt.Errorf("version_regex did not match %q", ver)
	}
	if len(m) > 1 {
		return strings.TrimSpace(m[1]), nil
	}
	return strings.TrimSpace(m[0]), nil
}

func applyVersionRewrite(ver string, rewrite []string) (string, error) {
	if len(rewrite) != 2 {
		return ver, nil
	}
	re, err := regexp.Compile(rewrite[0])
	if err != nil {
		return "", fmt.Errorf("invalid version_rewrite regex: %w", err)
	}
	return re.ReplaceAllString(ver, rewrite[1]), nil
}

func detectViaFilename(cfg LocalConfig) (string, error) {
	if cfg.Path == "" {
		return "", fmt.Errorf("filename detector requires path")
	}
	if cfg.VersionRegex == "" {
		return "", fmt.Errorf("filename detector requires version_regex")
	}
	matches, err := filepath.Glob(cfg.Path)
	if err != nil {
		return "", fmt.Errorf("filename glob: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no files matched %s", cfg.Path)
	}
	re, err := regexp.Compile(cfg.VersionRegex)
	if err != nil {
		return "", fmt.Errorf("invalid version_regex: %w", err)
	}
	name := filepath.Base(matches[0])
	m := re.FindStringSubmatch(name)
	if m == nil {
		return "", fmt.Errorf("version_regex did not match filename %q", name)
	}
	if len(m) > 1 {
		return strings.TrimSpace(m[1]), nil
	}
	return strings.TrimSpace(m[0]), nil
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
