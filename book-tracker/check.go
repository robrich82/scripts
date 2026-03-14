package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const graceDays = 7

// cmdCheck prints volumes that are available (past release date + grace) but
// not yet owned in Calibre, plus any due reminders.
func cmdCheck(dir string) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	now := time.Now()
	cutoff := now.AddDate(0, 0, -graceDays) // available if date <= cutoff

	type available struct {
		name    string
		vol     int
		date    time.Time
		daysAgo int
	}

	var avail []available
	var noDate []string // active series with no known date

	for _, s := range st.Series {
		if s.Status != StatusActive {
			continue
		}
		if s.NextVolumeDate == nil {
			noDate = append(noDate, fmt.Sprintf("  %s vol.%d", s.Name, s.NextVolume))
			continue
		}
		if !s.NextVolumeDate.After(cutoff) {
			// Check if already owned.
			if ownsVolume(s, s.NextVolume) {
				continue
			}
			daysAgo := int(now.Sub(*s.NextVolumeDate).Hours() / 24)
			avail = append(avail, available{s.Name, s.NextVolume, *s.NextVolumeDate, daysAgo})
		}
	}

	// Sort by date ascending.
	sort.Slice(avail, func(i, j int) bool {
		return avail[i].date.Before(avail[j].date)
	})

	// --- Print available volumes ---
	if len(avail) > 0 {
		fmt.Println("=== AVAILABLE (not in Calibre) ===")
		for _, a := range avail {
			age := ""
			if a.daysAgo == 0 {
				age = "today"
			} else if a.daysAgo == 1 {
				age = "1 day ago"
			} else {
				age = fmt.Sprintf("%d days ago", a.daysAgo)
			}
			fmt.Printf("  %-55s vol.%d  (%s, %s)\n",
				a.name, a.vol, a.date.Format("2006-01-02"), age)
		}
		fmt.Println()
	}

	// --- Print upcoming (within 60 days) ---
	var upcoming []available
	horizon := now.AddDate(0, 0, 60)
	for _, s := range st.Series {
		if s.Status != StatusActive || s.NextVolumeDate == nil {
			continue
		}
		if s.NextVolumeDate.After(cutoff) && !s.NextVolumeDate.After(horizon) {
			daysUntil := int(s.NextVolumeDate.Sub(now).Hours()/24) + 1
			upcoming = append(upcoming, available{s.Name, s.NextVolume, *s.NextVolumeDate, -daysUntil})
		}
	}
	sort.Slice(upcoming, func(i, j int) bool {
		return upcoming[i].date.Before(upcoming[j].date)
	})
	if len(upcoming) > 0 {
		fmt.Println("=== COMING SOON (next 60 days) ===")
		for _, u := range upcoming {
			days := -u.daysAgo
			fmt.Printf("  %-55s vol.%d  (%s, in %d days)\n",
				u.name, u.vol, u.date.Format("2006-01-02"), days)
		}
		fmt.Println()
	}

	// --- Reminders ---
	reminded := false
	checkReminder := func(name string, r *Reminder) bool {
		if r == nil {
			return false
		}
		if r.LastReminded != nil && now.Sub(*r.LastReminded).Hours() < float64(r.IntervalDays*24) {
			return false
		}
		return true
	}

	for _, s := range st.Series {
		if checkReminder(s.Name, s.Reminder) {
			if !reminded {
				fmt.Println("=== REMINDERS ===")
				reminded = true
			}
			fmt.Printf("  [%s] %s\n", s.Name, s.Reminder.Message)
		}
	}
	for _, c := range st.Completed {
		if checkReminder(c.Name, c.Reminder) {
			if !reminded {
				fmt.Println("=== REMINDERS ===")
				reminded = true
			}
			fmt.Printf("  [%s] %s\n", c.Name, c.Reminder.Message)
		}
	}
	if reminded {
		fmt.Println()
	}

	// --- No-date series ---
	if len(noDate) > 0 {
		fmt.Println("=== NO RELEASE DATE (run 'book-tracker update' to fetch) ===")
		fmt.Println(strings.Join(noDate, "\n"))
		fmt.Println()
	}

	if len(avail) == 0 && len(upcoming) == 0 && !reminded && len(noDate) == 0 {
		fmt.Println("Nothing to report.")
	}

	// Persist updated reminder timestamps.
	save := false
	for _, s := range st.Series {
		if checkReminder(s.Name, s.Reminder) {
			now2 := now
			s.Reminder.LastReminded = &now2
			save = true
		}
	}
	for _, c := range st.Completed {
		if checkReminder(c.Name, c.Reminder) {
			now2 := now
			c.Reminder.LastReminded = &now2
			save = true
		}
	}
	if save {
		return SaveStore(dir, st)
	}
	return nil
}

func ownsVolume(s *Series, vol int) bool {
	for _, v := range s.OwnedVolumes {
		if v == vol {
			return true
		}
	}
	return false
}
