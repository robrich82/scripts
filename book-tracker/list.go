package main

import (
	"fmt"
	"sort"
)

// cmdList prints all tracked and completed series.
func cmdList(dir string) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	// Group active series by status.
	byStatus := map[SeriesStatus][]*Series{}
	for _, s := range st.Series {
		byStatus[s.Status] = append(byStatus[s.Status], s)
	}
	for _, group := range byStatus {
		sort.Slice(group, func(i, j int) bool {
			return group[i].Name < group[j].Name
		})
	}

	printGroup := func(title string, series []*Series) {
		if len(series) == 0 {
			return
		}
		fmt.Printf("=== %s ===\n", title)
		for _, s := range series {
			date := "???"
			if s.NextVolumeDate != nil {
				date = s.NextVolumeDate.Format("2006-01-02")
			}
			muID := ""
			if s.MangaUpdatesID != "" {
				muID = fmt.Sprintf(" [MU:%s]", s.MangaUpdatesID)
			}
			owned := ""
			if len(s.OwnedVolumes) > 0 {
				owned = fmt.Sprintf(" owned:%v", s.OwnedVolumes)
			}
			unofficial := ""
			if s.UnofficialInCalibre {
				unofficial = " [unofficial]"
			}
			fmt.Printf("  %-55s %-12s next:%-3d %s%s%s%s\n",
				s.Name, s.Type, s.NextVolume, date, owned, muID, unofficial)
			if s.Notes != "" {
				fmt.Printf("    note: %s\n", s.Notes)
			}
		}
		fmt.Println()
	}

	printGroup("ACTIVE", byStatus[StatusActive])
	printGroup("ONLINE ONLY", byStatus[StatusOnlineOnly])
	printGroup("JAPANESE ONLY", byStatus[StatusJPOnly])

	// Completed.
	if len(st.Completed) > 0 {
		completed := make([]*CompletedSeries, len(st.Completed))
		copy(completed, st.Completed)
		sort.Slice(completed, func(i, j int) bool {
			return completed[i].Name < completed[j].Name
		})
		fmt.Println("=== COMPLETED ===")
		for _, c := range completed {
			muID := ""
			if c.MangaUpdatesID != "" {
				muID = fmt.Sprintf(" [MU:%s]", c.MangaUpdatesID)
			}
			fmt.Printf("  %-55s %-12s %d vols%s\n",
				c.Name, c.Type, c.TotalVolumes, muID)
			if c.Notes != "" {
				fmt.Printf("    note: %s\n", c.Notes)
			}
		}
		fmt.Println()
	}

	// Summary.
	fmt.Printf("Tracking: %d active, %d jp-only, %d online-only | Completed: %d\n",
		len(byStatus[StatusActive]),
		len(byStatus[StatusJPOnly]),
		len(byStatus[StatusOnlineOnly]),
		len(st.Completed))

	return nil
}
