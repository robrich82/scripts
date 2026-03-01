package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/dhowden/tag"
)

// MusicVideoTagger copies tags and cover art from a source audio file to a
// destination MKV file.
type MusicVideoTagger interface {
	CopyTagsToMKV(audioPath, mkvPath, titleSuffix string) error
}

// AudioTags holds the metadata extracted from an audio file.
type AudioTags struct {
	Title       string
	Artist      string
	AlbumArtist string
	Album       string
	TrackNum    int
	Year        int
	Genre       string
	Comment     string
	CoverData   []byte
	CoverMIME   string
}

// readAudioTags opens path and reads its embedded metadata via dhowden/tag.
// The track number is normalised to an integer — dhowden/tag already strips
// any "/total" suffix stored by formats such as DSF.
func readAudioTags(path string) (AudioTags, error) {
	f, err := os.Open(path)
	if err != nil {
		return AudioTags{}, err
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return AudioTags{}, fmt.Errorf("read tags %s: %w", path, err)
	}

	trackNum, _ := m.Track()
	var coverData []byte
	var coverMIME string
	if pic := m.Picture(); pic != nil {
		coverData = pic.Data
		coverMIME = pic.MIMEType
	}

	return AudioTags{
		Title:       m.Title(),
		Artist:      m.Artist(),
		AlbumArtist: m.AlbumArtist(),
		Album:       m.Album(),
		TrackNum:    trackNum,
		Year:        m.Year(),
		Genre:       m.Genre(),
		Comment:     m.Comment(),
		CoverData:   coverData,
		CoverMIME:   coverMIME,
	}, nil
}

// buildTagsXML returns a Matroska tags XML document for the given AudioTags.
// All non-empty / non-zero fields are written as Simple tags at target level 50.
func buildTagsXML(t AudioTags) string {
	var elems []string
	add := func(name, value string) {
		if value != "" {
			elems = append(elems, "<Simple><Name>"+name+"</Name><String>"+xmlEscape(value)+"</String></Simple>")
		}
	}
	add("TITLE", t.Title)
	add("ARTIST", t.Artist)
	add("ALBUM_ARTIST", t.AlbumArtist)
	add("ALBUM", t.Album)
	if t.TrackNum > 0 {
		add("PART_NUMBER", strconv.Itoa(t.TrackNum))
	}
	if t.Year > 0 {
		add("DATE_RECORDED", strconv.Itoa(t.Year))
	}
	add("GENRE", t.Genre)
	add("COMMENT", t.Comment)

	return "<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>" +
		strings.Join(elems, "") +
		"</Tag></Tags>"
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

// realMusicVideoTagger implements MusicVideoTagger using mkvpropedit.
type realMusicVideoTagger struct {
	mkvpropeditPath string
}

func newRealMusicVideoTagger() (*realMusicVideoTagger, error) {
	path, err := exec.LookPath("mkvpropedit")
	if err != nil {
		return nil, fmt.Errorf("mkvpropedit not found: %w", err)
	}
	return &realMusicVideoTagger{mkvpropeditPath: path}, nil
}

// CopyTagsToMKV implements MusicVideoTagger.
// It reads tags from audioPath, optionally appends titleSuffix to the title
// (with a space separator), then writes tags and cover art to mkvPath.
func (t *realMusicVideoTagger) CopyTagsToMKV(audioPath, mkvPath, titleSuffix string) error {
	tags, err := readAudioTags(audioPath)
	if err != nil {
		return err
	}
	if titleSuffix != "" && tags.Title != "" {
		tags.Title = tags.Title + " " + titleSuffix
	}

	// Write tags XML to a temp file.
	xmlFile, err := os.CreateTemp("", "mkv-musicvideo-tags-*.xml")
	if err != nil {
		return fmt.Errorf("create temp tags file: %w", err)
	}
	defer os.Remove(xmlFile.Name())
	if _, err := xmlFile.WriteString(buildTagsXML(tags)); err != nil {
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
		// fine — we swallow the error intentionally.
		exec.Command(t.mkvpropeditPath, mkvPath, "--delete-attachment", "name:cover.jpg").Run() //nolint:errcheck
		exec.Command(t.mkvpropeditPath, mkvPath, "--delete-attachment", "name:cover.png").Run() //nolint:errcheck
	}

	// Single mkvpropedit call: title header + tags + optional cover attachment.
	args := []string{mkvPath}
	if tags.Title != "" {
		args = append(args, "--edit", "info", "--set", "title="+tags.Title)
	}
	args = append(args, "--tags", "all:"+xmlFile.Name())
	if coverTmpPath != "" {
		args = append(args,
			"--attachment-name", coverName,
			"--attachment-mime-type", tags.CoverMIME,
			"--add-attachment", coverTmpPath,
		)
	}
	if err := exec.Command(t.mkvpropeditPath, args...).Run(); err != nil {
		return fmt.Errorf("mkvpropedit: %w", err)
	}
	return nil
}
