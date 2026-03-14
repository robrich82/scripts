package main

import (
	"fmt"
	"os"
	"strings"
)

const usage = `book-tracker — track manga and light novel releases

Commands:
  check              Show available volumes and reminders  (default)
  update             Fetch latest release dates from MangaUpdates
  calibre [DB_PATH]  Sync owned volumes from Calibre library
  plex               Suggest series from Plex anime library
  suggest            Suggest related/sequel series for tracked entries
  import FILE        Import series from Excel spreadsheet (one-time)
  add                Interactively add a new series
  list               List all tracked and completed series

Environment:
  PLEX_URL    Plex server base URL  (e.g. http://localhost:32400)
  PLEX_TOKEN  Plex authentication token
`

func main() {
	dir := dataDir()

	args := os.Args[1:]
	cmd := "check"
	if len(args) > 0 {
		cmd = strings.ToLower(args[0])
	}

	mu := newMUClient()
	al := newALClient()

	var err error
	switch cmd {
	case "check", "":
		err = cmdCheck(dir)

	case "update":
		err = cmdUpdate(dir, mu, true)

	case "calibre":
		dbPath := defaultCalibreDB
		if len(args) >= 2 {
			dbPath = args[1]
		}
		err = cmdCalibre(dir, dbPath, mu)

	case "plex":
		plexURL := os.Getenv("PLEX_URL")
		plexToken := os.Getenv("PLEX_TOKEN")
		if plexURL == "" || plexToken == "" {
			fmt.Fprintln(os.Stderr, "Error: PLEX_URL and PLEX_TOKEN must be set.")
			os.Exit(1)
		}
		plex := newPlexClient(plexURL, plexToken)
		err = cmdPlex(dir, plex, al, mu)

	case "suggest":
		err = cmdSuggest(dir, mu, al)

	case "import":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: book-tracker import FILE.xlsx")
			os.Exit(1)
		}
		err = cmdImport(dir, args[1], mu)

	case "add":
		err = cmdAdd(dir, mu)

	case "list":
		err = cmdList(dir)

	case "help", "--help", "-h":
		fmt.Print(usage)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q\n\n%s", cmd, usage)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
