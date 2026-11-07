package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func cmdInstall(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sw-update install <name>")
	}
	name := args[0]

	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	app := cfg.FindApp(name)
	if app == nil {
		return fmt.Errorf("app %q not found", name)
	}

	// Apps with a custom update command self-update in place rather than
	// downloading a release asset.
	if app.Update != nil && app.Update.Command != "" {
		return runUpdate(name, app)
	}

	client := &http.Client{Timeout: 30 * time.Second, Transport: checkTransport}
	checker := NewChecker(client)

	fmt.Printf("Checking latest version of %s...\n", name)
	result, err := checker.Check(*app)
	if err != nil {
		return fmt.Errorf("check %s: %w", name, err)
	}
	fmt.Printf("Latest version: %s\n", result.Version)

	if len(result.Assets) == 0 {
		if app.Checker.URL != "" {
			fmt.Printf("No downloadable assets found. Visit: %s\n", app.Checker.URL)
		} else {
			fmt.Printf("No downloadable assets found.\n")
		}
		return nil
	}

	downloadDir, err := cfg.DefaultDownloadDir()
	if err != nil {
		return err
	}
	destDir := filepath.Join(downloadDir, name)

	fmt.Printf("\nAssets to download into %s:\n", destDir)
	for i, a := range result.Assets {
		fmt.Printf("  %d. %s\n", i+1, a.Name)
	}

	if len(result.Assets) > 3 {
		fmt.Printf("\nNote: %d assets matched. Edit asset_patterns in config to narrow this down.\n",
			len(result.Assets))
	}

	reader := bufio.NewReader(os.Stdin)
	if !confirm(reader, "Proceed with download?", false) {
		fmt.Println("Cancelled.")
		return nil
	}

	referer := app.URL
	if referer == "" {
		referer = app.Checker.URL
	}

	var downloaded []string
	var failed []string
	for _, asset := range result.Assets {
		path, err := downloadAsset(asset, destDir, referer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error downloading %s: %v\n", asset.Name, err)
			failed = append(failed, asset.Name)
			continue
		}
		downloaded = append(downloaded, path)
	}

	if len(downloaded) == 0 {
		return fmt.Errorf("all downloads failed")
	}

	if len(failed) > 0 {
		fmt.Printf("\nNot downloaded: %s\n", strings.Join(failed, ", "))
	}
	var openFailed []string
	if confirm(reader, "Run/open downloaded file(s)?", true) {
		for _, path := range downloaded {
			if err := runOrOpen(path, app.ArchivePassword); err != nil {
				fmt.Fprintf(os.Stderr, "  Error opening %s: %v\n", filepath.Base(path), err)
				openFailed = append(openFailed, filepath.Base(path))
			}
		}
	}

	var markErr error
	if app.Local.Type == "" || app.Local.Type == "none" {
		// After a failed download or run/extract, default to not marking it installed.
		ok := len(failed) == 0 && len(openFailed) == 0
		if confirm(reader, "Mark version as installed/seen?", ok) {
			if err := markInstalled(name, result.Version); err != nil {
				markErr = fmt.Errorf("could not mark %q as version %s: %w", name, result.Version, err)
			} else {
				fmt.Printf("Marked %q as version %s\n", name, result.Version)
			}
		}
	}

	var downloadErr error
	if len(failed) > 0 {
		downloadErr = fmt.Errorf("%d of %d assets failed to download: %s",
			len(failed), len(result.Assets), strings.Join(failed, ", "))
	}
	var openErr error
	if len(openFailed) > 0 {
		openErr = fmt.Errorf("%d of %d files failed to run or extract: %s",
			len(openFailed), len(downloaded), strings.Join(openFailed, ", "))
	}
	return errors.Join(downloadErr, openErr, markErr)
}

// confirm asks a yes/no question and reads one line from reader. It accepts
// y/yes and n/no in any case, and an empty line gives def. Anything else, or
// end of input with no answer, is no: a closed stdin must not launch files.
func confirm(reader *bufio.Reader, question string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Printf("\n%s %s: ", question, hint)
	line, err := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	switch {
	case answer == "y" || answer == "yes":
		return true
	case answer == "" && err == nil:
		return def
	default:
		return false
	}
}

// markInstalled records version as both the latest and the last seen version
// of app name in the state file.
func markInstalled(name, version string) error {
	state, err := LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	st := state.Get(name)
	st.LastSeenVersion = version
	st.LatestVersion = version
	if err := SaveState(state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// runUpdate runs an app's configured custom update command after confirmation.
func runUpdate(name string, app *App) error {
	command, args := buildUpdateInvocation(app.Update)
	display := command
	if len(args) > 0 {
		display += " " + strings.Join(args, " ")
	}
	fmt.Printf("Update command for %s:\n  %s\n", name, display)
	if app.Update.Elevated {
		fmt.Println("  (will prompt for administrator elevation)")
	}

	if !confirm(bufio.NewReader(os.Stdin), "Run update command?", false) {
		fmt.Println("Cancelled.")
		return nil
	}

	if app.Update.Elevated {
		if err := runElevated(command, joinArgsForShell(args)); err != nil {
			return fmt.Errorf("update command failed: %w", err)
		}
		fmt.Println("\nUpdate started in an elevated window.")
		return nil
	}

	cmd := exec.Command(command, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("update command failed: %w", err)
	}
	fmt.Println("\nUpdate command finished.")
	return nil
}

// buildUpdateInvocation resolves the command and args to execute, routing
// .bat/.cmd scripts through cmd.exe on Windows (they cannot be exec'd directly).
func buildUpdateInvocation(u *UpdateConfig) (string, []string) {
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(u.Command)
		if strings.HasSuffix(lower, ".bat") || strings.HasSuffix(lower, ".cmd") {
			return "cmd", append([]string{"/c", u.Command}, u.Args...)
		}
	}
	return u.Command, u.Args
}

// joinArgsForShell joins args into a single command-line string, quoting any
// argument that contains whitespace (used when passing params to ShellExecuteW).
func joinArgsForShell(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			parts[i] = `"` + a + `"`
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}
