package main

import (
	"fmt"
	"sort"
)

// cmdMissing prints all owned-volume gaps for active and completed series.
// A "missing" volume is one between 1 and the last released volume that is
// not present in Calibre.
func cmdMissing(dir string) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	type entry struct {
		name    string
		missing []int
	}

	var rows []entry

	for _, s := range st.Series {
		if s.Status != StatusActive {
			continue
		}
		// Volumes 1..NextVolume-1 have been released; anything not owned is missing.
		top := s.NextVolume - 1
		if top <= 0 {
			continue
		}
		owned := ownedSet(s.OwnedVolumes)
		var gaps []int
		for v := 1; v <= top; v++ {
			if !owned[v] {
				gaps = append(gaps, v)
			}
		}
		if len(gaps) > 0 {
			rows = append(rows, entry{s.Name, gaps})
		}
	}

	for _, c := range st.Completed {
		if c.TotalVolumes <= 0 {
			continue
		}
		// For completed series we don't have OwnedVolumes — skip if no data.
		// (Completed entries don't carry OwnedVolumes; they only store TotalVolumes.)
	}

	if len(rows) == 0 {
		fmt.Println("No missing volumes found.")
		return nil
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	fmt.Println("=== MISSING VOLUMES ===")
	for _, r := range rows {
		fmt.Printf("  %-55s  %s\n", r.name, formatVolumes(r.missing))
	}
	return nil
}

// ownedSet converts a volume slice into a presence map.
func ownedSet(vols []int) map[int]bool {
	m := make(map[int]bool, len(vols))
	for _, v := range vols {
		m[v] = true
	}
	return m
}

// formatVolumes formats a sorted slice of volume numbers as compact ranges,
// e.g. [1 2 3 5 6 9] → "vol.1–3, 5–6, 9".
func formatVolumes(vols []int) string {
	if len(vols) == 0 {
		return ""
	}
	type span struct{ lo, hi int }
	var spans []span
	lo := vols[0]
	hi := vols[0]
	for _, v := range vols[1:] {
		if v == hi+1 {
			hi = v
		} else {
			spans = append(spans, span{lo, hi})
			lo, hi = v, v
		}
	}
	spans = append(spans, span{lo, hi})

	out := ""
	for i, sp := range spans {
		if i > 0 {
			out += ", "
		}
		if sp.lo == sp.hi {
			out += fmt.Sprintf("vol.%d", sp.lo)
		} else {
			out += fmt.Sprintf("vol.%d–%d", sp.lo, sp.hi)
		}
	}
	return out
}
