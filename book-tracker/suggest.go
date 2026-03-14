package main

import (
	"fmt"
	"strconv"
	"strings"
)

// cmdSuggest finds related series (sequels, prequels, side stories, spin-offs)
// for all tracked series and prompts to add ones not already tracked.
func cmdSuggest(dir string, mu *MUClient, al *ALClient) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	changed := false

	for _, s := range st.Series {
		if s.MangaUpdatesID == "" && s.AniListID == 0 {
			continue
		}

		suggestions := gatherSuggestions(s, mu, al)
		if len(suggestions) == 0 {
			continue
		}

		for _, sug := range suggestions {
			if isTracked(st, sug.name) {
				continue
			}
			if isIgnored(s, sug.muID, sug.alID) {
				continue
			}

			fmt.Printf("\n  [suggest] %q has a related series: %q (%s)\n",
				s.Name, sug.name, sug.relType)

			if !confirmNo(fmt.Sprintf("  Add %q to tracking?", sug.name)) {
				// Remember to not ask again.
				key := ignoredKey(sug.muID, sug.alID)
				s.IgnoredSuggestions = append(s.IgnoredSuggestions, key)
				changed = true
				continue
			}

			newS := &Series{
				ID:         uniqueID(st, sug.name),
				Name:       sug.name,
				Type:       sug.typ,
				Status:     StatusActive,
				NextVolume: 1,
				AniListID:  sug.alID,
			}
			if sug.muID != "" {
				newS.MangaUpdatesID = sug.muID
			} else if mu != nil {
				fmt.Printf("  Searching MangaUpdates for %q...\n", sug.name)
				if ms := matchMangaUpdates(mu, sug.name, muPreferType(sug.typ)); ms != nil {
					newS.MangaUpdatesID = fmt.Sprintf("%d", ms.SeriesID)
				}
			}
			st.Series = append(st.Series, newS)
			fmt.Printf("  Added %q.\n", sug.name)
			changed = true
		}
	}

	if changed {
		return SaveStore(dir, st)
	}
	fmt.Println("No new suggestions found.")
	return nil
}

type suggestion struct {
	name    string
	relType string
	typ     SeriesType
	muID    string
	alID    int
}

func gatherSuggestions(s *Series, mu *MUClient, al *ALClient) []suggestion {
	var out []suggestion

	// MangaUpdates relations.
	if s.MangaUpdatesID != "" && mu != nil {
		id, err := strconv.ParseInt(s.MangaUpdatesID, 10, 64)
		if err == nil {
			series, err := mu.GetSeries(id)
			if err == nil {
				for _, rel := range RelevantRelations(series) {
					out = append(out, suggestion{
						name:    rel.RelationTitle,
						relType: rel.RelationType,
						typ:     guessType(s.Type, rel.RelationType),
						muID:    fmt.Sprintf("%d", rel.RelationID),
					})
				}
			}
		}
	}

	// AniList relations (as cross-check / supplement).
	if s.AniListID != 0 && al != nil {
		media, err := al.GetMedia(s.AniListID)
		if err == nil {
			for _, rel := range RelevantALRelations(media) {
				name := rel.Node.ALTitle()
				// Skip if already covered by MU.
				already := false
				for _, o := range out {
					if strings.EqualFold(o.name, name) {
						already = true
						break
					}
				}
				if already {
					continue
				}
				out = append(out, suggestion{
					name:    name,
					relType: rel.RelationType,
					typ:     alFormatToType(rel.Node.Format),
					alID:    rel.Node.ID,
				})
			}
		}
	}

	return out
}

// guessType infers whether a related series is the same type as the parent.
// For prequels/sequels it's usually the same; for adaptations it may differ.
func guessType(parentType SeriesType, relType string) SeriesType {
	return parentType // default: same type; user can correct via edit
}

func isIgnored(s *Series, muID string, alID int) bool {
	alKey := fmt.Sprintf("al:%d", alID)
	for _, ig := range s.IgnoredSuggestions {
		if muID != "" && ig == muID {
			return true
		}
		if alID != 0 && ig == alKey {
			return true
		}
	}
	return false
}

func ignoredKey(muID string, alID int) string {
	if muID != "" {
		return muID
	}
	return fmt.Sprintf("al:%d", alID)
}
