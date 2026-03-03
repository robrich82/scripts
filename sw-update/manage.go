package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func cmdAdd(args []string) error {
	reader := bufio.NewReader(os.Stdin)

	prompt := func(msg string) string {
		fmt.Print(msg)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(strings.TrimRight(line, "\r\n"))
	}

	name := prompt("App name: ")
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}

	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	if cfg.FindApp(name) != nil {
		return fmt.Errorf("app %q already exists", name)
	}

	url := prompt("Bookmark URL: ")
	checkerType := prompt("Checker type [github/html] (default: github): ")
	if checkerType == "" {
		checkerType = "github"
	}

	app := App{
		Name:    name,
		Enabled: true,
		URL:     url,
		Local:   LocalConfig{Type: "none"},
	}

	switch checkerType {
	case "github":
		repo := prompt("GitHub repo (owner/repo): ")
		preStr := prompt("Include pre-releases? [y/N]: ")
		patStr := prompt("Asset pattern(s), comma-separated (blank = auto): ")
		var patterns []string
		if strings.TrimSpace(patStr) != "" {
			for _, p := range strings.Split(patStr, ",") {
				if t := strings.TrimSpace(p); t != "" {
					patterns = append(patterns, t)
				}
			}
		}
		app.Checker = CheckerConfig{
			Type:          "github",
			Repo:          repo,
			Prerelease:    strings.EqualFold(strings.TrimSpace(preStr), "y"),
			AssetPatterns: patterns,
		}
	case "html":
		versionRegex := prompt("Version regex (first capture group = version): ")
		app.Checker = CheckerConfig{
			Type:         "html",
			URL:          url,
			VersionRegex: versionRegex,
		}
	default:
		return fmt.Errorf("unknown checker type %q", checkerType)
	}

	cfg.Apps = append(cfg.Apps, app)
	if err := SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Added %q\n", name)
	return nil
}

func cmdRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sw-update remove <name>")
	}
	name := args[0]

	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	newApps := cfg.Apps[:0]
	found := false
	for _, a := range cfg.Apps {
		if a.Name == name {
			found = true
		} else {
			newApps = append(newApps, a)
		}
	}
	if !found {
		return fmt.Errorf("app %q not found", name)
	}

	cfg.Apps = newApps
	if err := SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Removed %q\n", name)
	return nil
}

func cmdEnable(args []string) error {
	return setEnabled(args, true)
}

func cmdDisable(args []string) error {
	return setEnabled(args, false)
}

func setEnabled(args []string, enabled bool) error {
	if len(args) == 0 {
		verb := "enable"
		if !enabled {
			verb = "disable"
		}
		return fmt.Errorf("usage: sw-update %s <name>", verb)
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
	app.Enabled = enabled
	if err := SaveConfig(cfg); err != nil {
		return err
	}
	verb := "Enabled"
	if !enabled {
		verb = "Disabled"
	}
	fmt.Printf("%s %q\n", verb, name)
	return nil
}

func cmdMark(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sw-update mark <name>")
	}
	name := args[0]

	state, err := LoadState()
	if err != nil {
		return err
	}
	st := state.Get(name)
	if st.LatestVersion == "" {
		return fmt.Errorf("%q has no known latest version; run 'sw-update check %s' first", name, name)
	}
	st.LastSeenVersion = st.LatestVersion
	if err := SaveState(state); err != nil {
		return err
	}
	fmt.Printf("Marked %q as seen at version %s\n", name, st.LatestVersion)
	return nil
}
