package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cmdUpdate fetches latest release dates from MangaUpdates for all tracked
// series and checks whether any JP-only series have been licensed in English.
// If interactive is true, it prompts when a JP-only series is found to be
// licensed.
func cmdUpdate(dir string, mu *MUClient, interactive bool) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	now := time.Now()
	changed := false

	for _, s := range st.Series {
		if s.MangaUpdatesID == "" {
			continue
		}
		id, err := strconv.ParseInt(s.MangaUpdatesID, 10, 64)
		if err != nil {
			continue
		}

		fmt.Printf("  Checking %s...\n", s.Name)

		series, err := mu.GetSeries(id)
		if err != nil {
			fmt.Printf("  [!] %s: %v\n", s.Name, err)
			continue
		}

		s.LastAPICheck = &now

		switch s.Status {
		case StatusActive, StatusOnlineOnly:
			// Try to fill in next-volume date if unknown.
			if s.NextVolumeDate == nil {
				date := mu.VolumeRelease(id, s.NextVolume)
				if date != nil {
					s.NextVolumeDate = date
					fmt.Printf("  [+] %s vol.%d date: %s\n",
						s.Name, s.NextVolume, date.Format("2006-01-02"))
					changed = true
				}
			}

			// Advance tracker if we now own the current next volume.
			// (Calibre sync does the detailed advance; here we just notice
			// if MangaUpdates shows a newer volume exists.)
			latestVol, latestDate, err := mu.LatestEnglishRelease(id)
			if err == nil && latestVol > s.NextVolume {
				// A newer volume has been released that we haven't tracked yet.
				fmt.Printf("  [!] %s: vol.%d is out (we're tracking vol.%d)\n",
					s.Name, latestVol, s.NextVolume)
			}
			_ = latestDate

		case StatusJPOnly:
			// Check if an English publisher has been announced.
			pub := EnglishPublisher(series)
			if pub != "" || series.Licensed {
				fmt.Printf("\n  [NEW LICENSE] %s is now licensed in English", s.Name)
				if pub != "" {
					fmt.Printf(" by %s", pub)
				}
				fmt.Println(".")

				if interactive && confirmNo(fmt.Sprintf("  Move %q to active tracking?", s.Name)) {
					s.Status = StatusActive
					s.NextVolume = 1
					fmt.Printf("  Set %s to active. Run 'update' again to fetch vol.1 date.\n", s.Name)
					changed = true
				}
			}
		}

		// Sync name from MangaUpdates (official English title, or Japanese if unlicensed).
		if muName := muBestName(series); muName != "" && muName != s.Name {
			fmt.Printf("  [~] %s: updating name to %q\n", s.Name, muName)
			// Preserve Calibre matching under the old name if not already overridden.
			if s.CalibreSeries == "" {
				s.CalibreSeries = s.Name
			}
			s.Name = muName
			changed = true
		}

		// Track MU completion status.
		if IsComplete(series) {
			if !s.MUComplete {
				fmt.Printf("  [i] %s is complete in MangaUpdates.\n", s.Name)
				s.MUComplete = true
				changed = true
			}
		} else if s.MUComplete {
			s.MUComplete = false
			changed = true
		}

		changed = true // always persist LastAPICheck
	}

	// Check original-format alerts: does a tracked series have a counterpart
	// format available that we haven't noticed yet?
	if interactive {
		checkOtherFormat(st, mu, &changed)
	}

	if changed {
		return SaveStore(dir, st)
	}
	return nil
}

// checkOtherFormat looks up each series on MangaUpdates and checks whether the
// counterpart format (LN for Manga series, Manga for LN series) exists and has
// an English release, prompting to add it.
func checkOtherFormat(st *Store, mu *MUClient, changed *bool) {
	for _, s := range st.Series {
		if s.MangaUpdatesID == "" || s.WantBoth {
			continue
		}
		id, err := strconv.ParseInt(s.MangaUpdatesID, 10, 64)
		if err != nil {
			continue
		}
		series, err := mu.GetSeries(id)
		if err != nil {
			continue
		}

		// Determine what the original format is.
		originalType := muTypeToSeriesType(series.Type)
		if originalType == "" {
			continue
		}

		var counterpartRelType string
		if s.Type == TypeManga && originalType == TypeLightNovel {
			counterpartRelType = "original" // manga is adaptation of LN
		} else if s.Type == TypeLightNovel && originalType == TypeManga {
			counterpartRelType = "original" // LN is adaptation of manga
		}

		// Also check related series for an adaptation link.
		for _, rel := range series.RelatedSeries {
			lower := strings.ToLower(rel.RelationType)
			if !strings.Contains(lower, "adapt") && !strings.Contains(lower, "original") {
				continue
			}
			if counterpartRelType == "" {
				counterpartRelType = lower
			}

			// Check if this related series is already tracked.
			alreadyTracked := false
			for _, other := range st.Series {
				if other.MangaUpdatesID == fmt.Sprintf("%d", rel.RelationID) {
					alreadyTracked = true
					break
				}
			}
			if alreadyTracked {
				continue
			}

			// Check if ignored.
			ignored := false
			for _, ig := range s.IgnoredSuggestions {
				if ig == fmt.Sprintf("%d", rel.RelationID) {
					ignored = true
					break
				}
			}
			if ignored {
				continue
			}

			fmt.Printf("\n  [FORMAT] %s also has a related series: %q (%s)\n",
				s.Name, rel.RelationTitle, rel.RelationType)
			if confirmNo(fmt.Sprintf("  Add %q to tracking?", rel.RelationTitle)) {
				// Determine type from MU.
				relSeries, err := mu.GetSeries(rel.RelationID)
				if err != nil {
					fmt.Printf("  [!] Could not fetch: %v\n", err)
					continue
				}
				newType := muTypeToSeriesType(relSeries.Type)
				if newType == "" {
					newType = s.Type // fallback
				}
				newS := &Series{
					ID:             uniqueID(st, rel.RelationTitle),
					Name:           rel.RelationTitle,
					Type:           newType,
					Status:         StatusActive,
					NextVolume:     1,
					MangaUpdatesID: fmt.Sprintf("%d", rel.RelationID),
					RelatedID:      s.ID,
					OriginalFormat: originalType,
				}
				if confirmNo("  Track both formats (want_both)?") {
					newS.WantBoth = true
					s.WantBoth = true
					newS.RelatedID = s.ID
				}
				st.Series = append(st.Series, newS)
				*changed = true
				fmt.Printf("  Added %q.\n", rel.RelationTitle)
			} else {
				// Remember to not ask again.
				s.IgnoredSuggestions = append(s.IgnoredSuggestions,
					fmt.Sprintf("%d", rel.RelationID))
				*changed = true
			}
		}
	}
}

func muTypeToSeriesType(muType string) SeriesType {
	switch strings.ToLower(muType) {
	case "manga", "manhwa", "manhua", "doujinshi":
		return TypeManga
	case "novel":
		return TypeLightNovel
	}
	return ""
}
