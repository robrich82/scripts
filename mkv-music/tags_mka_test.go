package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkvextract writes a DOCTYPE declaration before the <Tags> root; verify it
// does not prevent parsing.
const mkvExtractXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE Tags SYSTEM "matroskatags.dtd">
<Tags>
  <Tag>
    <Targets><TargetTypeValue>50</TargetTypeValue></Targets>
    <Simple><Name>TITLE</Name><String>Jagged Little Pill</String></Simple>
    <Simple><Name>ARTIST</Name><String>Alanis Morissette</String></Simple>
    <Simple><Name>DATE_RECORDED</Name><String>1995</String></Simple>
    <Simple><Name>GENRE</Name><String>Alternative Rock</String></Simple>
  </Tag>
  <Tag>
    <Targets><TargetTypeValue>30</TargetTypeValue></Targets>
    <Simple><Name>TITLE</Name><String>You Oughta Know</String></Simple>
    <Simple><Name>ARTIST</Name><String>Alanis Morissette</String></Simple>
    <Simple><Name>PART_NUMBER</Name><String>2</String></Simple>
  </Tag>
</Tags>`

func TestParseMKVTagsXML_TwoLevels(t *testing.T) {
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "tags.xml")
	os.WriteFile(xmlPath, []byte(mkvExtractXML), 0644)

	lvl50, lvl30 := parseMKVTagsXML(xmlPath)

	checks50 := map[string]string{
		"TITLE":         "Jagged Little Pill",
		"ARTIST":        "Alanis Morissette",
		"DATE_RECORDED": "1995",
		"GENRE":         "Alternative Rock",
	}
	for k, want := range checks50 {
		if got := lvl50[k]; got != want {
			t.Errorf("lvl50[%s] = %q, want %q", k, got, want)
		}
	}

	checks30 := map[string]string{
		"TITLE":       "You Oughta Know",
		"ARTIST":      "Alanis Morissette",
		"PART_NUMBER": "2",
	}
	for k, want := range checks30 {
		if got := lvl30[k]; got != want {
			t.Errorf("lvl30[%s] = %q, want %q", k, got, want)
		}
	}
}

func TestParseMKVTagsXML_DefaultsToLevel50(t *testing.T) {
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "tags.xml")
	// No TargetTypeValue → should default to level 50.
	content := `<Tags>
  <Tag>
    <Targets></Targets>
    <Simple><Name>TITLE</Name><String>Some Album</String></Simple>
  </Tag>
</Tags>`
	os.WriteFile(xmlPath, []byte(content), 0644)

	lvl50, _ := parseMKVTagsXML(xmlPath)
	if lvl50["TITLE"] != "Some Album" {
		t.Errorf("default level should be 50, got TITLE=%q", lvl50["TITLE"])
	}
}

func TestParseMKVTagsXML_MissingFile(t *testing.T) {
	lvl50, lvl30 := parseMKVTagsXML("/nonexistent/file.xml")
	if len(lvl50) != 0 || len(lvl30) != 0 {
		t.Errorf("expected empty maps for missing file, got lvl50=%v lvl30=%v", lvl50, lvl30)
	}
}

func TestParseMKVTagsXML_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "empty.xml")
	os.WriteFile(xmlPath, []byte{}, 0644)

	lvl50, lvl30 := parseMKVTagsXML(xmlPath)
	if len(lvl50) != 0 || len(lvl30) != 0 {
		t.Errorf("expected empty maps for empty file")
	}
}

// --- buildHeaderTitle tests ---

func TestBuildHeaderTitle_Full(t *testing.T) {
	tags := AudioTags{AlbumArtist: "The Goo Goo Dolls", Album: "Dizzy Up the Girl", Title: "Iris"}
	got := buildHeaderTitle(tags, "")
	if got != "The Goo Goo Dolls - Dizzy Up the Girl - Iris" {
		t.Errorf("got %q", got)
	}
}

func TestBuildHeaderTitle_WithSuffix(t *testing.T) {
	tags := AudioTags{AlbumArtist: "The Cardigans", Album: "Emmerdale", Title: "Rise & Shine"}
	got := buildHeaderTitle(tags, "(Second Version)")
	if got != "The Cardigans - Emmerdale - Rise & Shine (Second Version)" {
		t.Errorf("got %q", got)
	}
}

func TestBuildHeaderTitle_FallsBackToArtist(t *testing.T) {
	tags := AudioTags{Artist: "Alanis Morissette", Album: "Jagged Little Pill", Title: "Ironic"}
	got := buildHeaderTitle(tags, "")
	if got != "Alanis Morissette - Jagged Little Pill - Ironic" {
		t.Errorf("got %q", got)
	}
}

func TestBuildHeaderTitle_MissingParts(t *testing.T) {
	got := buildHeaderTitle(AudioTags{Title: "Song"}, "")
	if got != "Song" {
		t.Errorf("got %q", got)
	}
	got = buildHeaderTitle(AudioTags{}, "")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestBuildTagsXML_ReplayGainPreserved(t *testing.T) {
	tags := AudioTags{
		Title:       "You Oughta Know",
		Artist:      "Alanis Morissette",
		AlbumArtist: "Alanis Morissette",
		Album:       "Jagged Little Pill",
		TrackNum:    2,
		Date:        "1995",
		Genre:       "Alternative Rock",
	}
	extra50 := map[string]string{
		"REPLAYGAIN_ALBUM_GAIN": "-6.50 dB",
		"REPLAYGAIN_ALBUM_PEAK": "0.987654",
		"R128_ALBUM_GAIN":       "-650",
	}
	extra30 := map[string]string{
		"REPLAYGAIN_TRACK_GAIN": "-7.20 dB",
		"REPLAYGAIN_TRACK_PEAK": "0.999000",
		"R128_TRACK_GAIN":       "-720",
	}

	xml := buildTagsXML(tags, extra50, extra30)

	for _, want := range []string{
		"REPLAYGAIN_ALBUM_GAIN", "-6.50 dB",
		"REPLAYGAIN_ALBUM_PEAK", "0.987654",
		"R128_ALBUM_GAIN", "-650",
		"REPLAYGAIN_TRACK_GAIN", "-7.20 dB",
		"REPLAYGAIN_TRACK_PEAK", "0.999000",
		"R128_TRACK_GAIN", "-720",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("buildTagsXML output missing %q", want)
		}
	}
}

func TestBuildTagsXML_NilExtraMaps(t *testing.T) {
	tags := AudioTags{Title: "Song", Album: "Album"}
	// Should not panic with nil extra maps.
	xml := buildTagsXML(tags, nil, nil)
	if !strings.Contains(xml, "Song") {
		t.Errorf("expected title in output, got %q", xml)
	}
}

func TestParseMKVTagsXML_ReplayGainRoundTrip(t *testing.T) {
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "tags.xml")
	content := `<Tags>
  <Tag>
    <Targets><TargetTypeValue>50</TargetTypeValue></Targets>
    <Simple><Name>TITLE</Name><String>Some Album</String></Simple>
    <Simple><Name>REPLAYGAIN_ALBUM_GAIN</Name><String>-6.50 dB</String></Simple>
    <Simple><Name>R128_ALBUM_GAIN</Name><String>-650</String></Simple>
  </Tag>
  <Tag>
    <Targets><TargetTypeValue>30</TargetTypeValue></Targets>
    <Simple><Name>TITLE</Name><String>Some Track</String></Simple>
    <Simple><Name>REPLAYGAIN_TRACK_GAIN</Name><String>-7.20 dB</String></Simple>
    <Simple><Name>R128_TRACK_GAIN</Name><String>-720</String></Simple>
  </Tag>
</Tags>`
	os.WriteFile(xmlPath, []byte(content), 0644)

	lvl50, lvl30 := parseMKVTagsXML(xmlPath)

	checks := []struct{ level, key, want string }{
		{"50", "REPLAYGAIN_ALBUM_GAIN", "-6.50 dB"},
		{"50", "R128_ALBUM_GAIN", "-650"},
		{"30", "REPLAYGAIN_TRACK_GAIN", "-7.20 dB"},
		{"30", "R128_TRACK_GAIN", "-720"},
	}
	for _, c := range checks {
		m := lvl50
		if c.level == "30" {
			m = lvl30
		}
		if got := m[c.key]; got != c.want {
			t.Errorf("lvl%s[%s] = %q, want %q", c.level, c.key, got, c.want)
		}
	}
}

func TestParseMKVTagsXML_FirstValueWins(t *testing.T) {
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "tags.xml")
	// Two level-50 tags — first TITLE value should win.
	content := `<Tags>
  <Tag>
    <Targets><TargetTypeValue>50</TargetTypeValue></Targets>
    <Simple><Name>TITLE</Name><String>First</String></Simple>
  </Tag>
  <Tag>
    <Targets><TargetTypeValue>50</TargetTypeValue></Targets>
    <Simple><Name>TITLE</Name><String>Second</String></Simple>
  </Tag>
</Tags>`
	os.WriteFile(xmlPath, []byte(content), 0644)

	lvl50, _ := parseMKVTagsXML(xmlPath)
	if lvl50["TITLE"] != "First" {
		t.Errorf("first value should win, got %q", lvl50["TITLE"])
	}
}
