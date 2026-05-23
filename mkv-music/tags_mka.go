package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// flexInt64 unmarshals a JSON number or an RFC3339 string into a Unix timestamp (seconds).
// Newer mkvmerge versions emit date_utc as an ISO 8601 string instead of an integer.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexInt64(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	*f = flexInt64(t.Unix())
	return nil
}

// mkvTagsDocument decodes the XML produced by: mkvextract <file> tags <output>
// mkvextract always includes a DOCTYPE declaration; Go's encoding/xml skips it.
type mkvTagsDocument struct {
	XMLName xml.Name `xml:"Tags"`
	Tags    []struct {
		Targets struct {
			TargetTypeValue string `xml:"TargetTypeValue"`
		} `xml:"Targets"`
		Simple []struct {
			Name   string `xml:"Name"`
			String string `xml:"String"`
		} `xml:"Simple"`
	} `xml:"Tag"`
}

// parseMKVTagsXML reads a Matroska tags XML file and returns two maps:
//   - level50: album-level tags (TargetTypeValue=50, or absent which defaults to 50)
//   - level30: track-level tags (TargetTypeValue=30)
//
// Keys are upper-case tag names (TITLE, ARTIST, …).
// Missing or empty files return two empty maps without error.
func parseMKVTagsXML(xmlPath string) (level50, level30 map[string]string) {
	level50 = make(map[string]string)
	level30 = make(map[string]string)

	data, err := os.ReadFile(xmlPath)
	if err != nil || len(data) == 0 {
		return
	}

	var doc mkvTagsDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return
	}

	for _, tag := range doc.Tags {
		target := level50 // Matroska default TargetTypeValue is 50
		if tag.Targets.TargetTypeValue == "30" {
			target = level30
		}
		for _, s := range tag.Simple {
			name := strings.ToUpper(s.Name)
			if target[name] == "" { // first value wins
				target[name] = s.String
			}
		}
	}
	return
}

// mkvmergeIdentify is the subset of `mkvmerge --identification-format json`
// output needed to discover image attachments and the file header title.
type mkvmergeIdentify struct {
	Container struct {
		Properties struct {
			Title   string     `json:"title"`
			DateUTC *flexInt64 `json:"date_utc"`
		} `json:"properties"`
	} `json:"container"`
	Attachments []struct {
		ID          int    `json:"id"`
		ContentType string `json:"content_type"`
	} `json:"attachments"`
}

// readMKVHeaderTitle returns the file-header title stored in an MKV, or ""
// when the title is absent or the identify call fails.
func readMKVHeaderTitle(mkvmergePath, mkvPath string) string {
	var buf bytes.Buffer
	cmd := exec.Command(mkvmergePath, "--identify", "--identification-format", "json", mkvPath)
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return ""
	}
	var info mkvmergeIdentify
	if err := json.Unmarshal(buf.Bytes(), &info); err != nil {
		return ""
	}
	return info.Container.Properties.Title
}

// readMKVHeaderDate returns the date stored in the MKV container header.
// date_utc from mkvmerge JSON is a Unix timestamp in seconds (UTC).
// Returns zero time and false when absent or on error.
func readMKVHeaderDate(mkvmergePath, mkvPath string) (time.Time, bool) {
	var buf bytes.Buffer
	cmd := exec.Command(mkvmergePath, "--identify", "--identification-format", "json", mkvPath)
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return time.Time{}, false
	}
	var info mkvmergeIdentify
	if err := json.Unmarshal(buf.Bytes(), &info); err != nil {
		return time.Time{}, false
	}
	if info.Container.Properties.DateUTC == nil {
		return time.Time{}, false
	}
	return time.Unix(int64(*info.Container.Properties.DateUTC), 0).UTC(), true
}

// extractMKACover finds the first image attachment in mkaPath and returns its
// raw bytes and MIME type.  Returns nil, "" when no image attachment exists.
func extractMKACover(mkvextractPath, mkvmergePath, mkaPath string) ([]byte, string) {
	var identBuf bytes.Buffer
	idCmd := exec.Command(mkvmergePath, "--identify", "--identification-format", "json", mkaPath)
	idCmd.Stdout = &identBuf
	idCmd.Stderr = io.Discard
	if idCmd.Run() != nil {
		return nil, ""
	}

	var info mkvmergeIdentify
	if err := json.Unmarshal(identBuf.Bytes(), &info); err != nil {
		return nil, ""
	}

	for _, att := range info.Attachments {
		if !strings.HasPrefix(att.ContentType, "image/") {
			continue
		}
		tmp, err := os.CreateTemp("", "mka-cover-*")
		if err != nil {
			continue
		}
		tmp.Close()
		defer os.Remove(tmp.Name())

		extract := exec.Command(mkvextractPath, mkaPath, "attachments",
			fmt.Sprintf("%d:%s", att.ID, tmp.Name()))
		extract.Stdout = io.Discard
		extract.Stderr = io.Discard
		if extract.Run() != nil {
			continue
		}
		data, err := os.ReadFile(tmp.Name())
		if err == nil && len(data) > 0 {
			return data, att.ContentType
		}
	}
	return nil, ""
}

// readAudioTagsFromMKA reads metadata from a Matroska Audio (.mka) file using
// mkvextract (for global tags) and mkvmerge (for attachment discovery).
// dhowden/tag does not support the Matroska container format.
func readAudioTagsFromMKA(path, mkvextractPath, mkvmergePath string) (AudioTags, error) {
	xmlTmp, err := os.CreateTemp("", "mka-tags-*.xml")
	if err != nil {
		return AudioTags{}, fmt.Errorf("create temp tags file: %w", err)
	}
	xmlTmp.Close()
	defer os.Remove(xmlTmp.Name())

	// mkvextract exits non-zero when the file has no tags — tolerated.
	cmd := exec.Command(mkvextractPath, path, "tags", xmlTmp.Name())
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Run() //nolint:errcheck

	lvl50, lvl30 := parseMKVTagsXML(xmlTmp.Name())

	var trackNum int
	if n, err := strconv.Atoi(lvl30["PART_NUMBER"]); err == nil {
		trackNum = n
	}
	dateStr := normalizeDate(lvl50["DATE_RECORDED"])
	if dateStr == "" {
		dateStr = normalizeDate(lvl30["DATE_RECORDED"])
	}
	// Fall back to the MKV container header date when DATE_RECORDED is absent.
	if dateStr == "" {
		if dt, ok := readMKVHeaderDate(mkvmergePath, path); ok {
			dateStr = dt.UTC().Format("2006-01-02")
		}
	}
	var year int
	if dt, ok := parseAudioDate(dateStr); ok {
		year = dt.Year()
	}

	coverData, coverMIME := extractMKACover(mkvextractPath, mkvmergePath, path)

	return AudioTags{
		Title:       lvl30["TITLE"],
		Artist:      lvl30["ARTIST"],
		AlbumArtist: lvl50["ARTIST"],
		Album:       lvl50["TITLE"],
		TrackNum:    trackNum,
		Year:        year,
		Date:        dateStr,
		Genre:       lvl50["GENRE"],
		Comment:     lvl50["COMMENT"],
		CoverData:   coverData,
		CoverMIME:   coverMIME,
	}, nil
}

// extractReplayGainTags reads existing ReplayGain/R128 tags from mkvPath and
// returns them keyed by level. Returns empty maps on any error.
func extractReplayGainTags(mkvextractPath, mkvPath string) (extra50, extra30 map[string]string) {
	extra50 = make(map[string]string)
	extra30 = make(map[string]string)

	tmp, err := os.CreateTemp("", "mkv-existing-tags-*.xml")
	if err != nil {
		return
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	cmd := exec.Command(mkvextractPath, mkvPath, "tags", tmp.Name())
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Run() //nolint:errcheck

	lvl50, lvl30 := parseMKVTagsXML(tmp.Name())
	for _, k := range replayGainKeys50 {
		if v := lvl50[k]; v != "" {
			extra50[k] = v
		}
	}
	for _, k := range replayGainKeys30 {
		if v := lvl30[k]; v != "" {
			extra30[k] = v
		}
	}
	return
}
