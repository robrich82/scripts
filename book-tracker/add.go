package main

import (
	"fmt"
	"strings"
)

// cmdAdd interactively adds a new series.
func cmdAdd(dir string, mu *MUClient) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	name := ask("Series name: ")
	if name == "" {
		return fmt.Errorf("name required")
	}

	fmt.Print("Type [m]anga / [l]ight novel: ")
	typAns := ask("")
	var typ SeriesType
	switch strings.ToLower(strings.TrimSpace(typAns)) {
	case "l", "light novel", "ln":
		typ = TypeLightNovel
	default:
		typ = TypeManga
	}

	fmt.Print("Status [a]ctive / [j]p-only / [o]nline-only: ")
	stAns := strings.ToLower(strings.TrimSpace(ask("")))
	var status SeriesStatus
	switch stAns {
	case "j", "jp", "jp-only":
		status = StatusJPOnly
	case "o", "online", "online-only":
		status = StatusOnlineOnly
	default:
		status = StatusActive
	}

	var nextVol int
	if status == StatusActive || status == StatusOnlineOnly {
		fmt.Sscanf(ask("Next volume number: "), "%d", &nextVol)
		if nextVol < 1 {
			nextVol = 1
		}
	}

	notes := ask("Notes (optional): ")

	s := &Series{
		ID:         uniqueID(st, name),
		Name:       name,
		Type:       typ,
		Status:     status,
		NextVolume: nextVol,
		Notes:      notes,
	}

	if status == StatusOnlineOnly {
		s.Reminder = &Reminder{
			Message:      fmt.Sprintf("Check for fan-compiled volumes of %s", name),
			IntervalDays: 90,
		}
	}

	unofficial := false
	if status == StatusJPOnly {
		unofficial = confirmNo("Fan-translated copies already in Calibre?")
		s.UnofficialInCalibre = unofficial
	}

	// MangaUpdates lookup.
	if mu != nil {
		fmt.Printf("Searching MangaUpdates for %q...\n", name)
		if ms := matchMangaUpdates(mu, name); ms != nil {
			s.MangaUpdatesID = fmt.Sprintf("%d", ms.SeriesID)
		}
	}

	st.Series = append(st.Series, s)
	if err := SaveStore(dir, st); err != nil {
		return err
	}
	fmt.Printf("Added %q (%s, %s).\n", name, typ, status)
	return nil
}
