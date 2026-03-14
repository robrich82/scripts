package main

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const defaultCalibreDB = `C:\Users\robri\OneDrive\Calibre Library\metadata.db`

// calibreBook is a row from the Calibre library.
type calibreBook struct {
	id          int
	title       string
	seriesName  string
	seriesIndex float64 // Calibre uses float for series index
	tags        []string
}

// openCalibre opens the Calibre metadata.db in read-only mode.
func openCalibre(path string) (*sql.DB, error) {
	// Open with immutable flag to avoid locking Calibre's live database.
	return sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1&_journal=wal")
}

// loadCalibreBooks returns all books that have a series and are tagged
// "Manga" or "Light Novel".
func loadCalibreBooks(dbPath string) ([]calibreBook, error) {
	db, err := openCalibre(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	const q = `
SELECT b.id, b.title, COALESCE(s.name,''), b.series_index,
       GROUP_CONCAT(t.name, '|') as tags
FROM books b
LEFT JOIN books_series_link bsl ON bsl.book = b.id
LEFT JOIN series s ON s.id = bsl.series
LEFT JOIN books_tags_link btl ON btl.book = b.id
LEFT JOIN tags t ON t.id = btl.tag
GROUP BY b.id
HAVING s.name != '' AND (tags LIKE '%Manga%' OR tags LIKE '%Light Novel%')
ORDER BY s.name, b.series_index`

	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("calibre query: %w", err)
	}
	defer rows.Close()

	var books []calibreBook
	for rows.Next() {
		var bk calibreBook
		var tagsConcat string
		if err := rows.Scan(&bk.id, &bk.title, &bk.seriesName, &bk.seriesIndex, &tagsConcat); err != nil {
			continue
		}
		bk.tags = strings.Split(tagsConcat, "|")
		books = append(books, bk)
	}
	return books, rows.Err()
}

// cmdCalibre syncs owned volumes from Calibre, prompts about untracked series,
// and advances the NextVolume counter for series where the current one is owned.
func cmdCalibre(dir, dbPath string, mu *MUClient) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	books, err := loadCalibreBooks(dbPath)
	if err != nil {
		return fmt.Errorf("reading Calibre: %w", err)
	}

	// Group books by series name → set of volume numbers.
	type seriesInfo struct {
		volumes []int
		tags    []string
	}
	byName := map[string]*seriesInfo{}
	for _, bk := range books {
		si := byName[bk.seriesName]
		if si == nil {
			si = &seriesInfo{tags: bk.tags}
			byName[bk.seriesName] = si
		}
		vol := int(math.Round(bk.seriesIndex))
		si.volumes = append(si.volumes, vol)
	}

	changed := false

	// --- Update owned volumes for tracked series ---
	for _, s := range st.Series {
		if s.UnofficialInCalibre {
			continue // don't count fan translations
		}
		key := s.CalibreKey()
		si := byName[key]
		if si == nil {
			// Try case-insensitive fallback.
			for k, v := range byName {
				if strings.EqualFold(k, key) {
					si = v
					break
				}
			}
		}
		if si == nil {
			continue
		}

		sort.Ints(si.volumes)
		if !intSliceEqual(s.OwnedVolumes, si.volumes) {
			s.OwnedVolumes = si.volumes
			changed = true
			fmt.Printf("  [calibre] %s: owned volumes %v\n", s.Name, si.volumes)
		}

		// Advance NextVolume if we now own it.
		if ownsVolume(s, s.NextVolume) {
			s.NextVolume++
			s.NextVolumeDate = nil // unknown until update fetches it
			fmt.Printf("  [+] %s: advanced to vol.%d\n", s.Name, s.NextVolume)
			changed = true
		}
	}

	// --- Check for completion ---
	for _, s := range st.Series {
		if s.Status != StatusActive || s.MangaUpdatesID == "" {
			continue
		}
		// A series is completed if MangaUpdates says it's done and we own all
		// volumes through the highest one.
		if len(s.OwnedVolumes) == 0 {
			continue
		}
		sort.Ints(s.OwnedVolumes)
		maxOwned := s.OwnedVolumes[len(s.OwnedVolumes)-1]

		// Simple heuristic: if we own up to the last volume and NextVolume
		// would be beyond what MU reports as the final volume, mark complete.
		// We rely on the user confirming.
		if s.MUComplete && maxOwned >= s.NextVolume-1 {
			// Ask the user.
			fmt.Printf("\n  %s: you own vol.1–%d and next expected is vol.%d.\n",
				s.Name, maxOwned, s.NextVolume)
			if confirmNo(fmt.Sprintf("  Mark %q as completed?", s.Name)) {
				completed := &CompletedSeries{
					ID:             s.ID,
					Name:           s.Name,
					Type:           s.Type,
					TotalVolumes:   maxOwned,
					Notes:          s.Notes,
					CompletedDate:  time.Now(),
					MangaUpdatesID: s.MangaUpdatesID,
					AniListID:      s.AniListID,
					Reminder:       s.Reminder,
				}
				st.Completed = append(st.Completed, completed)
				// Remove from active.
				st.Series = removeSeries(st.Series, s.ID)
				fmt.Printf("  Moved %q to completed.\n", s.Name)
				changed = true
			}
		}
	}

	// --- Untracked Calibre series ---
	for calName, si := range byName {
		if isTracked(st, calName) {
			continue
		}
		sort.Ints(si.volumes)
		typ := calibreTagToType(si.tags)

		fmt.Printf("\n  [untracked] Calibre series: %q (%s, vols %v)\n", calName, typ, si.volumes)
		fmt.Println("  Options: [a]dd to active  [c]omplete  [j]p-only  [f]an-translated  [s]kip")
		ans := ask("  Choice: ")
		switch strings.ToLower(strings.TrimSpace(ans)) {
		case "a":
			maxVol := si.volumes[len(si.volumes)-1]
			s := &Series{
				ID:           uniqueID(st, calName),
				Name:         calName,
				Type:         typ,
				Status:       StatusActive,
				NextVolume:   maxVol + 1,
				OwnedVolumes: si.volumes,
			}
			if mu != nil {
				fmt.Printf("  Searching MangaUpdates for %q...\n", calName)
				if ms := matchMangaUpdates(mu, calName, muPreferType(typ)); ms != nil {
					s.MangaUpdatesID = fmt.Sprintf("%d", ms.SeriesID)
				}
			}
			st.Series = append(st.Series, s)
			fmt.Printf("  Added %q as active.\n", calName)
			changed = true
		case "c":
			maxVol := si.volumes[len(si.volumes)-1]
			c := &CompletedSeries{
				ID:            uniqueID(st, calName),
				Name:          calName,
				Type:          typ,
				TotalVolumes:  maxVol,
				CompletedDate: time.Now(),
			}
			st.Completed = append(st.Completed, c)
			fmt.Printf("  Added %q to completed.\n", calName)
			changed = true
		case "j":
			s := &Series{
				ID:     uniqueID(st, calName),
				Name:   calName,
				Type:   typ,
				Status: StatusJPOnly,
			}
			st.Series = append(st.Series, s)
			fmt.Printf("  Added %q as jp-only.\n", calName)
			changed = true
		case "f":
			s := &Series{
				ID:                  uniqueID(st, calName),
				Name:                calName,
				Type:                typ,
				Status:              StatusJPOnly,
				UnofficialInCalibre: true,
				Notes:               "Fan-translated copies in Calibre",
			}
			if mu != nil {
				fmt.Printf("  Searching MangaUpdates for %q...\n", calName)
				if ms := matchMangaUpdates(mu, calName, muPreferType(typ)); ms != nil {
					s.MangaUpdatesID = fmt.Sprintf("%d", ms.SeriesID)
				}
			}
			st.Series = append(st.Series, s)
			fmt.Printf("  Added %q as jp-only (unofficial copies noted).\n", calName)
			changed = true
		default:
			fmt.Printf("  Skipped %q.\n", calName)
		}
	}

	if changed {
		return SaveStore(dir, st)
	}
	fmt.Println("Calibre sync complete, no changes.")
	return nil
}

// --- helpers -----------------------------------------------------------------

func isTracked(st *Store, calName string) bool {
	lower := strings.ToLower(calName)
	for _, s := range st.Series {
		if strings.ToLower(s.CalibreKey()) == lower {
			return true
		}
	}
	for _, c := range st.Completed {
		if strings.ToLower(c.Name) == lower {
			return true
		}
	}
	return false
}

func calibreTagToType(tags []string) SeriesType {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), "Light Novel") {
			return TypeLightNovel
		}
	}
	return TypeManga
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func removeSeries(slice []*Series, id string) []*Series {
	out := slice[:0]
	for _, s := range slice {
		if s.ID != id {
			out = append(out, s)
		}
	}
	return out
}
