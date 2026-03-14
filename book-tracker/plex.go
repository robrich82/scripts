package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PlexClient handles read-only calls to the Plex Media Server.
type PlexClient struct {
	baseURL string
	token   string
	hc      *http.Client
}

func newPlexClient(baseURL, token string) *PlexClient {
	return &PlexClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *PlexClient) get(path string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("X-Plex-Token", p.token)
	u := p.baseURL + path + "?" + params.Encode()
	resp, err := p.hc.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("plex %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type plexSections struct {
	MediaContainer struct {
		Directory []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
			Type  string `json:"type"`
		} `json:"Directory"`
	} `json:"MediaContainer"`
}

type plexLibrary struct {
	MediaContainer struct {
		Metadata []struct {
			Title  string `json:"title"`
			Type   string `json:"type"`
			Studio string `json:"studio"`
		} `json:"Metadata"`
	} `json:"MediaContainer"`
}

// TVShows returns all show titles from sections of type "show".
func (p *PlexClient) TVShows() ([]string, error) {
	var sections plexSections
	if err := p.get("/library/sections", nil, &sections); err != nil {
		return nil, err
	}

	var titles []string
	for _, sec := range sections.MediaContainer.Directory {
		if sec.Type != "show" {
			continue
		}
		var lib plexLibrary
		params := url.Values{"type": {"2"}} // type=2 = TV shows
		if err := p.get("/library/sections/"+sec.Key+"/all", params, &lib); err != nil {
			fmt.Printf("  [!] Plex section %q: %v\n", sec.Title, err)
			continue
		}
		for _, m := range lib.MediaContainer.Metadata {
			titles = append(titles, m.Title)
		}
	}
	return titles, nil
}

// cmdPlex scans the Plex TV library, identifies anime via AniList, and
// suggests related manga/LN series not yet tracked.
func cmdPlex(dir string, plex *PlexClient, al *ALClient, mu *MUClient) error {
	st, err := LoadStore(dir)
	if err != nil {
		return err
	}

	fmt.Println("Fetching TV library from Plex...")
	titles, err := plex.TVShows()
	if err != nil {
		return fmt.Errorf("plex: %w", err)
	}
	fmt.Printf("Found %d shows. Checking for anime and related manga/LN...\n\n", len(titles))

	changed := false

	for _, title := range titles {
		// Look up in AniList to confirm it's anime and find source material.
		anime, err := al.SearchAnime(title)
		if err != nil || anime == nil {
			continue // not found in AniList → not anime (or ambiguous)
		}

		source := SourceMedia(anime)
		if source == nil {
			continue // no source material link
		}
		// Only care about manga/novel sources.
		if source.Format != "MANGA" && source.Format != "NOVEL" && source.Format != "ONE_SHOT" {
			continue
		}

		sourceName := source.ALTitle()
		if isTracked(st, sourceName) {
			continue
		}

		// Check if it's in the ignored list of any existing entry.
		alIDStr := fmt.Sprintf("al:%d", source.ID)
		ignored := false
		for _, s := range st.Series {
			for _, ig := range s.IgnoredSuggestions {
				if ig == alIDStr {
					ignored = true
					break
				}
			}
		}
		if ignored {
			continue
		}

		fmt.Printf("  [Plex] %q → source: %q (%s, %s)\n",
			title, sourceName, source.Format, source.Status)

		if !confirmNo(fmt.Sprintf("  Add %q to tracking?", sourceName)) {
			// Mark ignored on a placeholder; use the first series as anchor.
			if len(st.Series) > 0 {
				st.Series[0].IgnoredSuggestions = append(st.Series[0].IgnoredSuggestions, alIDStr)
				changed = true
			}
			continue
		}

		typ := alFormatToType(source.Format)
		newS := &Series{
			ID:         uniqueID(st, sourceName),
			Name:       sourceName,
			Type:       typ,
			Status:     StatusActive,
			NextVolume: 1,
			AniListID:  source.ID,
		}

		// Try MangaUpdates match.
		if mu != nil {
			fmt.Printf("  Searching MangaUpdates for %q...\n", sourceName)
			if ms := matchMangaUpdates(mu, sourceName, muPreferType(typ)); ms != nil {
				newS.MangaUpdatesID = fmt.Sprintf("%d", ms.SeriesID)
			}
		}

		st.Series = append(st.Series, newS)
		fmt.Printf("  Added %q.\n", sourceName)
		changed = true
	}

	if changed {
		return SaveStore(dir, st)
	}
	fmt.Println("Plex check complete.")
	return nil
}

func alFormatToType(format string) SeriesType {
	if format == "NOVEL" {
		return TypeLightNovel
	}
	return TypeManga
}
