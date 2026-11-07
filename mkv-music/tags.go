package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.senan.xyz/taglib"
)

// MusicVideoTagger copies tags and cover art from a source audio file to a
// destination MKV file, and can check whether the MKV tags already match.
type MusicVideoTagger interface {
	// TagsMatchMKV returns true if the MKV at mkvPath already has tags
	// identical to what CopyTagsToMKV would write from audioPath.
	// Returns false (not true) on any error so the caller falls back to copying.
	TagsMatchMKV(audioPath, mkvPath, titleSuffix string) (bool, error)
	CopyTagsToMKV(audioPath, mkvPath, titleSuffix string) error
	// MKVDate returns the date stored in the MKV container header and true,
	// or zero time and false when absent or on error.
	MKVDate(mkvPath string) (time.Time, bool)
}

// AudioTags holds the metadata extracted from an audio file.
type AudioTags struct {
	Title       string
	Artist      string
	AlbumArtist string
	Album       string
	TrackNum    int
	Year        int
	Date        string // full date string: "YYYY-MM-DD", "YYYY-MM", or "YYYY"
	Genre       string
	Comment     string
	CoverData   []byte
	CoverMIME   string
}

// readAudioTags opens path and reads its embedded metadata via go-taglib.
func readAudioTags(path string) (AudioTags, error) {
	tags, err := taglib.ReadTags(path)
	if err != nil {
		return AudioTags{}, fmt.Errorf("read tags %s: %w", path, err)
	}

	artist := firstTag(tags, taglib.Artist, "PERFORMER")
	albumArtist := firstTag(tags, taglib.AlbumArtist, "ALBUM_ARTIST")

	dateStr := normalizeDate(firstTag(tags, taglib.Date))
	var year int
	if dateStr != "" {
		if dt, ok := parseAudioDate(dateStr); ok {
			year = dt.Year()
		}
	}
	if year == 0 {
		if y, err := strconv.Atoi(firstTag(tags, taglib.Date)); err == nil && y > 0 {
			year = y
			dateStr = strconv.Itoa(y)
		}
	}

	trackNum := 0
	if s := firstTag(tags, taglib.TrackNumber); s != "" {
		// TRACKNUMBER may be "3" or "3/12"
		trackNum, _ = strconv.Atoi(strings.SplitN(s, "/", 2)[0])
	}

	coverData, _ := taglib.ReadImage(path)
	coverMIME := ""
	if len(coverData) > 0 {
		coverMIME = imageMIMEType(coverData)
	}

	return AudioTags{
		Title:       firstTag(tags, taglib.Title),
		Artist:      artist,
		AlbumArtist: albumArtist,
		Album:       firstTag(tags, taglib.Album),
		TrackNum:    trackNum,
		Year:        year,
		Date:        dateStr,
		Genre:       firstTag(tags, taglib.Genre),
		Comment:     firstTag(tags, taglib.Comment),
		CoverData:   coverData,
		CoverMIME:   coverMIME,
	}, nil
}

// firstTag returns the first non-empty value for any of the given keys.
func firstTag(tags map[string][]string, keys ...string) string {
	for _, k := range keys {
		for _, v := range tags[k] {
			if v != "" {
				return v
			}
		}
	}
	return ""
}

// imageMIMEType detects image/jpeg or image/png from magic bytes.
func imageMIMEType(data []byte) string {
	if bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}) {
		return "image/png"
	}
	return "image/jpeg"
}

// normalizeDate strips time components and validates the date string, returning
// a clean "YYYY-MM-DD", "YYYY-MM", or "YYYY" string, or "" if unrecognised.
func normalizeDate(s string) string {
	if i := strings.IndexByte(s, 'T'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if _, err := time.Parse(layout, s); err == nil {
			return s
		}
	}
	return ""
}

// parseAudioDate parses a date string produced by normalizeDate into a
// time.Time (UTC, day/month defaulting to 1). Returns false if unparseable.
func parseAudioDate(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// replayGainKeys50 and replayGainKeys30 are album- and track-level ReplayGain/R128
// tag names preserved when rewriting MKV tags. These are not copied from the source
// audio file because loudness normalisation is performed separately on the video.
var (
	replayGainKeys50 = []string{"REPLAYGAIN_ALBUM_GAIN", "REPLAYGAIN_ALBUM_PEAK", "R128_ALBUM_GAIN", "R128_ALBUM_PEAK", "REPLAYGAIN_GAIN", "REPLAYGAIN_PEAK"}
	replayGainKeys30 = []string{"REPLAYGAIN_TRACK_GAIN", "REPLAYGAIN_TRACK_PEAK", "R128_TRACK_GAIN", "R128_TRACK_PEAK", "REPLAYGAIN_GAIN", "REPLAYGAIN_PEAK"}
)

// buildTagsXML returns a Matroska tags XML document for the given AudioTags.
// extra50 and extra30 are additional tags (e.g. ReplayGain) to append at the
// album and track levels respectively; pass nil maps to omit.
//
// Per the Matroska spec, TITLE at TargetTypeValue=50 means the album/collection
// title, and TITLE at TargetTypeValue=30 means the track title.  ARTIST at
// level 50 is the album artist; ARTIST at level 30 is the track artist.
// Using a single level-50 block with track-level fields causes tag viewers
// (e.g. Mp3tag) to misread them.
func buildTagsXML(t AudioTags, extra50, extra30 map[string]string) string {
	simple := func(name, value string) string {
		return "<Simple><Name>" + name + "</Name><String>" + xmlEscape(value) + "</String></Simple>"
	}

	var lvl50, lvl30 []string
	addTo := func(elems *[]string, name, value string) {
		if value != "" {
			*elems = append(*elems, simple(name, value))
		}
	}

	// Album-level tags (TargetTypeValue=50): TITLE=album title, ARTIST=album artist.
	addTo(&lvl50, "TITLE", t.Album)
	addTo(&lvl50, "ARTIST", t.AlbumArtist)
	addTo(&lvl50, "DATE_RECORDED", t.Date)
	addTo(&lvl50, "GENRE", t.Genre)
	addTo(&lvl50, "COMMENT", t.Comment)

	// Track-level tags (TargetTypeValue=30): TITLE=track title, ARTIST=track artist.
	addTo(&lvl30, "TITLE", t.Title)
	addTo(&lvl30, "ARTIST", t.Artist)
	if t.TrackNum > 0 {
		addTo(&lvl30, "PART_NUMBER", strconv.Itoa(t.TrackNum))
	}

	for _, k := range replayGainKeys50 {
		if v := extra50[k]; v != "" {
			addTo(&lvl50, k, v)
		}
	}
	for _, k := range replayGainKeys30 {
		if v := extra30[k]; v != "" {
			addTo(&lvl30, k, v)
		}
	}

	var sb strings.Builder
	sb.WriteString("<Tags>")
	if len(lvl50) > 0 {
		sb.WriteString("<Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>")
		for _, e := range lvl50 {
			sb.WriteString(e)
		}
		sb.WriteString("</Tag>")
	}
	if len(lvl30) > 0 {
		sb.WriteString("<Tag><Targets><TargetTypeValue>30</TargetTypeValue></Targets>")
		for _, e := range lvl30 {
			sb.WriteString(e)
		}
		sb.WriteString("</Tag>")
	}
	sb.WriteString("</Tags>")
	return sb.String()
}

// xmlEscape replaces XML special characters with their entity references.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// realMusicVideoTagger implements MusicVideoTagger using MKVToolNix utilities.
// mkvpropedit writes tags to MKV; mkvextract + mkvmerge read tags from MKA.
type realMusicVideoTagger struct {
	mkvpropeditPath string
	mkvextractPath  string
	mkvmergePath    string
}

func newRealMusicVideoTagger() (*realMusicVideoTagger, error) {
	propedit, err := exec.LookPath("mkvpropedit")
	if err != nil {
		return nil, fmt.Errorf("mkvpropedit not found: %w", err)
	}
	extract, err := exec.LookPath("mkvextract")
	if err != nil {
		return nil, fmt.Errorf("mkvextract not found: %w", err)
	}
	merge, err := exec.LookPath("mkvmerge")
	if err != nil {
		return nil, fmt.Errorf("mkvmerge not found: %w", err)
	}
	return &realMusicVideoTagger{
		mkvpropeditPath: propedit,
		mkvextractPath:  extract,
		mkvmergePath:    merge,
	}, nil
}

// MKVDate implements MusicVideoTagger.
func (t *realMusicVideoTagger) MKVDate(mkvPath string) (time.Time, bool) {
	return readMKVHeaderDate(t.mkvmergePath, mkvPath)
}

// TagsMatchMKV implements MusicVideoTagger.
// It reads tags from audioPath and the current tags embedded in mkvPath,
// then compares all text fields and cover art.
// Returns false on any error so the caller falls back to copying.
func (t *realMusicVideoTagger) TagsMatchMKV(audioPath, mkvPath, titleSuffix string) (bool, error) {
	var want AudioTags
	var err error
	if strings.EqualFold(filepath.Ext(audioPath), ".mka") {
		want, err = readAudioTagsFromMKA(audioPath, t.mkvextractPath, t.mkvmergePath)
	} else {
		want, err = readAudioTags(audioPath)
	}
	if err != nil {
		return false, err
	}
	if titleSuffix != "" && want.Title != "" {
		want.Title = want.Title + " " + titleSuffix
	}

	xmlTmp, err := os.CreateTemp("", "mkv-check-tags-*.xml")
	if err != nil {
		return false, nil
	}
	xmlTmp.Close()
	defer os.Remove(xmlTmp.Name())

	cmd := exec.Command(t.mkvextractPath, mkvPath, "tags", xmlTmp.Name())
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Run() //nolint:errcheck

	lvl50, lvl30 := parseMKVTagsXML(xmlTmp.Name())

	wantTrack := ""
	if want.TrackNum > 0 {
		wantTrack = strconv.Itoa(want.TrackNum)
	}

	if lvl30["TITLE"] != want.Title ||
		lvl30["ARTIST"] != want.Artist ||
		lvl30["PART_NUMBER"] != wantTrack ||
		lvl50["TITLE"] != want.Album ||
		lvl50["ARTIST"] != want.AlbumArtist ||
		lvl50["DATE_RECORDED"] != want.Date ||
		lvl50["GENRE"] != want.Genre ||
		lvl50["COMMENT"] != want.Comment {
		return false, nil
	}

	if readMKVHeaderTitle(t.mkvmergePath, mkvPath) != buildHeaderTitle(want, "") {
		return false, nil
	}

	// Check MKV header date matches the audio date.
	if wantDt, ok := parseAudioDate(want.Date); ok {
		mkvDate, hasMKVDate := readMKVHeaderDate(t.mkvmergePath, mkvPath)
		if !hasMKVDate || mkvDate.UTC().Format("2006-01-02") != wantDt.UTC().Format("2006-01-02") {
			return false, nil
		}
	}

	// Compare cover art: extract the current attachment from the MKV and check
	// it byte-for-byte against the source.
	mkvCover, _ := extractMKACover(t.mkvextractPath, t.mkvmergePath, mkvPath)
	return bytes.Equal(want.CoverData, mkvCover), nil
}

// CopyTagsToMKV implements MusicVideoTagger.
// It reads tags from audioPath, optionally appends titleSuffix to the title
// (with a space separator), then writes tags and cover art to mkvPath.
func (t *realMusicVideoTagger) CopyTagsToMKV(audioPath, mkvPath, titleSuffix string) error {
	var tags AudioTags
	var err error
	if strings.EqualFold(filepath.Ext(audioPath), ".mka") {
		tags, err = readAudioTagsFromMKA(audioPath, t.mkvextractPath, t.mkvmergePath)
	} else {
		tags, err = readAudioTags(audioPath)
	}
	if err != nil {
		return err
	}
	if titleSuffix != "" && tags.Title != "" {
		tags.Title = tags.Title + " " + titleSuffix
	}

	// Preserve ReplayGain tags already on the MKV — they differ from the source
	// audio file because loudness normalisation is run separately on the video.
	extra50, extra30 := extractReplayGainTags(t.mkvextractPath, mkvPath)

	// Write tags XML to a temp file.
	xmlFile, err := os.CreateTemp("", "mkv-musicvideo-tags-*.xml")
	if err != nil {
		return fmt.Errorf("create temp tags file: %w", err)
	}
	defer os.Remove(xmlFile.Name())
	if _, err := xmlFile.WriteString(buildTagsXML(tags, extra50, extra30)); err != nil {
		xmlFile.Close()
		return fmt.Errorf("write tags xml: %w", err)
	}
	xmlFile.Close()

	// Extract cover to a temp file, if present.
	var coverTmpPath, coverName string
	if len(tags.CoverData) > 0 {
		ext := ".jpg"
		if strings.Contains(tags.CoverMIME, "png") {
			ext = ".png"
		}
		coverFile, err := os.CreateTemp("", "mkv-musicvideo-cover-*"+ext)
		if err != nil {
			return fmt.Errorf("create temp cover file: %w", err)
		}
		defer os.Remove(coverFile.Name())
		if _, err := coverFile.Write(tags.CoverData); err != nil {
			coverFile.Close()
			return fmt.Errorf("write cover file: %w", err)
		}
		coverFile.Close()
		coverTmpPath = coverFile.Name()
		coverName = "cover" + ext

		// Remove any existing cover attachment before re-adding. These calls
		// will fail (non-zero) when the attachment doesn't exist, which is
		// fine — we swallow the error intentionally.  Stdout/stderr are
		// discarded to suppress the "The file is being analyzed." progress line.
		deleteAttachment := func(name string) {
			cmd := exec.Command(t.mkvpropeditPath, mkvPath, "--delete-attachment", "name:"+name)
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			cmd.Run() //nolint:errcheck
		}
		deleteAttachment("cover.jpg")
		deleteAttachment("cover.png")
	}

	// Single mkvpropedit call: title + date header + tags + optional cover attachment.
	args := []string{mkvPath}
	editInfo := false
	if headerTitle := buildHeaderTitle(tags, ""); headerTitle != "" {
		args = append(args, "--edit", "info", "--set", "title="+headerTitle)
		editInfo = true
	}
	if dt, ok := parseAudioDate(tags.Date); ok {
		if !editInfo {
			args = append(args, "--edit", "info")
		}
		args = append(args, "--set", fmt.Sprintf("date=%sT00:00:00Z", dt.Format("2006-01-02")))
	}
	args = append(args, "--tags", "all:"+xmlFile.Name())
	if coverTmpPath != "" {
		args = append(args,
			"--attachment-name", coverName,
			"--attachment-mime-type", tags.CoverMIME,
			"--add-attachment", coverTmpPath,
		)
	}
	cmd := exec.Command(t.mkvpropeditPath, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg != "" {
			return fmt.Errorf("mkvpropedit: %w\n%s", err, msg)
		}
		return fmt.Errorf("mkvpropedit: %w", err)
	}
	return nil
}

// buildHeaderTitle constructs the MKV file-header title in the form:
//
//	AlbumArtist - Album - Title
//
// AlbumArtist falls back to Artist when empty. Parts that are empty are omitted.
// titleSuffix (e.g. "(Second Version)") is appended to Title with a space.
func buildHeaderTitle(t AudioTags, titleSuffix string) string {
	artist := t.AlbumArtist
	if artist == "" {
		artist = t.Artist
	}
	songTitle := t.Title
	if titleSuffix != "" {
		songTitle = strings.TrimSpace(songTitle + " " + titleSuffix)
	}
	var parts []string
	for _, p := range []string{artist, t.Album, songTitle} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " - ")
}
