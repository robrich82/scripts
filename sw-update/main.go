package main

//go:generate go-winres make

import (
	"fmt"
	"os"
)

const usage = `sw-update — software update checker

Commands:
  check [-v] [name]  Check for updates (all enabled apps, or a specific app)
                     -v / --verbose: also show up-to-date apps
  list               List all configured apps and their known versions
  install <name>     Download latest release and run/open installer
  add                Add a new app interactively
  remove <name>      Remove an app from the config
  enable <name>      Enable an app for checking
  disable <name>     Disable an app without removing it
  mark <name>        Mark current latest as seen/installed (skip next update prompt)
  import <file>      Bootstrap config from a Netscape bookmarks HTML file

Environment:
  GITHUB_TOKEN       GitHub personal access token
                     Without it: 60 API requests/hour (shared by IP)
                     With it:  5000 requests/hour
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "check":
		err = cmdCheck(args)
	case "list":
		err = cmdList(args)
	case "install":
		err = cmdInstall(args)
	case "add":
		err = cmdAdd(args)
	case "remove":
		err = cmdRemove(args)
	case "enable":
		err = cmdEnable(args)
	case "disable":
		err = cmdDisable(args)
	case "mark":
		err = cmdMark(args)
	case "import":
		err = cmdImport(args)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n%s", cmd, usage)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
