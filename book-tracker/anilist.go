package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const anilistEndpoint = "https://graphql.anilist.co"

// ALClient talks to the AniList GraphQL API.
type ALClient struct{ hc *http.Client }

func newALClient() *ALClient {
	return &ALClient{hc: &http.Client{Timeout: 15 * time.Second}}
}

func (c *ALClient) query(q string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": q, "variables": vars})
	resp, err := c.hc.Post(anilistEndpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var wrapper struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return err
	}
	if len(wrapper.Errors) > 0 {
		return fmt.Errorf("AniList: %s", wrapper.Errors[0].Message)
	}
	return json.Unmarshal(wrapper.Data, out)
}

// ALMedia is a compact representation of an AniList media entry.
type ALMedia struct {
	ID     int    `json:"id"`
	Type   string `json:"type"`   // "ANIME", "MANGA"
	Format string `json:"format"` // "TV", "MANGA", "NOVEL", "ONE_SHOT", etc.
	Title  struct {
		Romaji  string `json:"romaji"`
		English string `json:"english"`
	} `json:"title"`
	Status    string `json:"status"` // "FINISHED", "RELEASING", "NOT_YET_RELEASED", etc.
	Volumes   int    `json:"volumes"`
	Chapters  int    `json:"chapters"`
	Relations struct {
		Edges []struct {
			RelationType string  `json:"relationType"` // "SOURCE", "ADAPTATION", "SEQUEL", "PREQUEL", "SIDE_STORY", "SPIN_OFF", "ALTERNATIVE"
			Node         ALMedia `json:"node"`
		} `json:"edges"`
	} `json:"relations"`
}

// ALTitle returns the best display title.
func (m *ALMedia) ALTitle() string {
	if m.Title.English != "" {
		return m.Title.English
	}
	return m.Title.Romaji
}

// SearchAnime finds an anime by title. Returns nil if not found.
func (c *ALClient) SearchAnime(title string) (*ALMedia, error) {
	const q = `
query($search: String) {
  Media(search: $search, type: ANIME) {
    id type format status
    title { romaji english }
    relations {
      edges {
        relationType
        node { id type format title { romaji english } status volumes }
      }
    }
  }
}`
	var data struct {
		Media *ALMedia `json:"Media"`
	}
	err := c.query(q, map[string]any{"search": title}, &data)
	if err != nil {
		// AniList returns a "Not Found" error as a GraphQL error; treat as nil.
		if strings.Contains(err.Error(), "Not Found") {
			return nil, nil
		}
		return nil, err
	}
	return data.Media, nil
}

// SearchManga finds a manga/novel by title. Returns nil if not found.
func (c *ALClient) SearchManga(title string) (*ALMedia, error) {
	const q = `
query($search: String) {
  Media(search: $search, type: MANGA) {
    id type format status volumes
    title { romaji english }
    relations {
      edges {
        relationType
        node { id type format title { romaji english } status volumes }
      }
    }
  }
}`
	var data struct {
		Media *ALMedia `json:"Media"`
	}
	err := c.query(q, map[string]any{"search": title}, &data)
	if err != nil {
		if strings.Contains(err.Error(), "Not Found") {
			return nil, nil
		}
		return nil, err
	}
	return data.Media, nil
}

// GetMedia fetches a media entry by AniList ID.
func (c *ALClient) GetMedia(id int) (*ALMedia, error) {
	const q = `
query($id: Int) {
  Media(id: $id) {
    id type format status volumes
    title { romaji english }
    relations {
      edges {
        relationType
        node { id type format title { romaji english } status volumes }
      }
    }
  }
}`
	var data struct {
		Media *ALMedia `json:"Media"`
	}
	if err := c.query(q, map[string]any{"id": id}, &data); err != nil {
		return nil, err
	}
	return data.Media, nil
}

// SourceMedia returns the source material of an anime (e.g. the manga it was
// adapted from), or nil.
func SourceMedia(anime *ALMedia) *ALMedia {
	for _, e := range anime.Relations.Edges {
		if e.RelationType == "SOURCE" {
			m := e.Node
			return &m
		}
	}
	return nil
}

// RelevantALRelations returns sequel/prequel/side-story/spin-off relations
// (same logic as MangaUpdates but for AniList relation types).
func RelevantALRelations(m *ALMedia) []struct {
	RelationType string
	Node         ALMedia
} {
	keep := map[string]bool{
		"SEQUEL": true, "PREQUEL": true,
		"SIDE_STORY": true, "SPIN_OFF": true,
		"ALTERNATIVE": false, // too vague – skip
	}
	var out []struct {
		RelationType string
		Node         ALMedia
	}
	for _, e := range m.Relations.Edges {
		if keep[e.RelationType] {
			out = append(out, struct {
				RelationType string
				Node         ALMedia
			}{e.RelationType, e.Node})
		}
	}
	return out
}
