package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// cmdImport imports series from the Excel spreadsheet into the store.
// It runs a MangaUpdates match for each series interactively, then saves.
// After import it automatically runs an update pass to fill in ??? dates.
func cmdImport(dir, xlsxPath string, mu *MUClient) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	f, err := excelize.OpenFile(xlsxPath)
	if err != nil {
		return fmt.Errorf("opening xlsx: %w", err)
	}
	defer f.Close()

	imported := 0

	// ---- English sheet -------------------------------------------------------
	rows, err := f.GetRows("English")
	if err != nil {
		return fmt.Errorf("reading English sheet: %w", err)
	}
	for i, row := range rows {
		if i == 0 || len(row) < 2 {
			continue // header
		}
		name := strings.TrimSpace(row[0])
		if name == "" {
			continue
		}
		if st.FindSeriesByName(name) != nil {
			fmt.Printf("  [skip] %q already tracked\n", name)
			continue
		}

		typ := parseType(row[1])
		status := StatusActive

		var nextVol int
		var nextDate *time.Time
		var notes string

		if len(row) >= 3 {
			fmt.Sscanf(strings.TrimSpace(row[2]), "%d", &nextVol)
		}
		if len(row) >= 4 {
			raw := strings.TrimSpace(row[3])
			if raw != "???" && raw != "" {
				// excelize returns dates as strings in "2006-01-02 15:04:05" form
				// after GetRows; try a few formats.
				for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02", "1/2/2006", "01/02/2006"} {
					if t, err := time.Parse(layout, raw); err == nil {
						tt := t
						nextDate = &tt
						break
					}
				}
			}
		}
		if len(row) >= 5 {
			notes = strings.TrimSpace(row[4])
		}

		// Detect online-only note.
		if strings.Contains(strings.ToLower(notes), "not officially available in volumes") {
			status = StatusOnlineOnly
		}

		s := &Series{
			ID:             uniqueID(st, name),
			Name:           name,
			Type:           typ,
			Status:         status,
			NextVolume:     nextVol,
			NextVolumeDate: nextDate,
			Notes:          notes,
		}

		// Online-only series get a quarterly reminder.
		if status == StatusOnlineOnly {
			s.Reminder = &Reminder{
				Message:      fmt.Sprintf("Check for fan-compiled volumes of %s", name),
				IntervalDays: 90,
			}
		}

		fmt.Printf("\n--- %s (%s) ---\n", name, typ)
		if mu != nil {
			if ms := matchMangaUpdates(mu, name); ms != nil {
				s.MangaUpdatesID = fmt.Sprintf("%d", ms.SeriesID)
			}
		}

		st.Series = append(st.Series, s)
		imported++
	}

	// ---- Japanese Only sheet -------------------------------------------------
	rows, err = f.GetRows("Japanese Only")
	if err != nil {
		return fmt.Errorf("reading Japanese Only sheet: %w", err)
	}
	for i, row := range rows {
		if i == 0 || len(row) < 2 {
			continue
		}
		name := strings.TrimSpace(row[0])
		if name == "" {
			continue
		}
		if st.FindSeriesByName(name) != nil {
			fmt.Printf("  [skip] %q already tracked\n", name)
			continue
		}

		typ := parseType(row[1])
		s := &Series{
			ID:     uniqueID(st, name),
			Name:   name,
			Type:   typ,
			Status: StatusJPOnly,
		}

		fmt.Printf("\n--- %s (%s) [JP Only] ---\n", name, typ)
		if mu != nil {
			if ms := matchMangaUpdates(mu, name); ms != nil {
				s.MangaUpdatesID = fmt.Sprintf("%d", ms.SeriesID)
			}
		}

		st.Series = append(st.Series, s)
		imported++
	}

	if err := SaveStore(dir, st); err != nil {
		return err
	}

	fmt.Printf("\nImported %d series.\n", imported)

	// Auto-run update to fill in ??? dates.
	fmt.Println("\nFetching release dates from MangaUpdates...")
	return cmdUpdate(dir, mu, false)
}

func parseType(s string) SeriesType {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "light novel") || strings.EqualFold(s, "Novel") {
		return TypeLightNovel
	}
	return TypeManga
}
