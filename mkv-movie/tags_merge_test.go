package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mkvextractXML is real mkvextract output after a previous apply: the
// file-level block has <Targets /> (mkvextract omits the default
// TargetTypeValue 50) and every value carries <TagLanguageIETF>.
const mkvextractXML = "\ufeff<?xml version=\"1.0\"?>\r\n<Tags>\r\n  <Tag>\r\n    <Targets />\r\n" +
	"    <Simple>\r\n      <Name>ENCODER</Name>\r\n      <String>Lavf</String>\r\n      <TagLanguageIETF>und</TagLanguageIETF>\r\n    </Simple>\r\n" +
	"    <Simple>\r\n      <Name>SUMMARY</Name>\r\n      <String>old</String>\r\n      <TagLanguageIETF>und</TagLanguageIETF>\r\n    </Simple>\r\n" +
	"    <Simple>\r\n      <Name>COUNTRY</Name>\r\n      <String>NZ</String>\r\n      <Simple>\r\n        <Name>LAW_RATING</Name>\r\n        <String>M</String>\r\n      </Simple>\r\n      <TagLanguageIETF>und</TagLanguageIETF>\r\n    </Simple>\r\n" +
	"    <Simple>\r\n      <Name>DEBAND</Name>\r\n      <String>true</String>\r\n      <TagLanguageIETF>und</TagLanguageIETF>\r\n    </Simple>\r\n" +
	"  </Tag>\r\n  <Tag>\r\n    <Targets>\r\n      <TrackUID>11</TrackUID>\r\n    </Targets>\r\n" +
	"    <Simple>\r\n      <Name>_STATISTICS_TAGS</Name>\r\n      <String>BPS</String>\r\n      <TagLanguageIETF>und</TagLanguageIETF>\r\n    </Simple>\r\n" +
	"  </Tag>\r\n</Tags>\r\n"

func TestUpdateDebandInXML_MkvextractOutput_RemovesDeband(t *testing.T) {
	if got := updateDebandInXML(mkvextractXML, false); strings.Contains(got, "DEBAND") {
		t.Errorf("DEBAND not removed:\n%s", got)
	}
}

func TestUpdateDebandInXML_MkvextractOutput_NoDuplicate(t *testing.T) {
	if n := strings.Count(updateDebandInXML(mkvextractXML, true), "DEBAND"); n != 1 {
		t.Errorf("DEBAND count = %d, want 1", n)
	}
}

func TestIsFileLevelTag(t *testing.T) {
	cases := map[string]bool{
		`<Tag><Targets /></Tag>`: true,
		`<Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets></Tag>`: true,
		`<Tag><Simple><Name>X</Name></Simple></Tag>`:                          true,
		`<Tag><Targets><TargetTypeValue>70</TargetTypeValue></Targets></Tag>`: false,
		`<Tag><Targets><TrackUID>1</TrackUID></Targets></Tag>`:                false,
	}
	for block, want := range cases {
		if got := isFileLevelTag(block); got != want {
			t.Errorf("isFileLevelTag(%s) = %v, want %v", block, got, want)
		}
	}
}

func TestMergeMovieTags_MkvextractOutput_ReplacesManagedKeepsOthers(t *testing.T) {
	got := mergeMovieTags(mkvextractXML, buildMovieTagXML("new", "7", "", RatingR13, false))
	for _, gone := range []string{">old<", ">M<", "DEBAND"} {
		if strings.Contains(got, gone) {
			t.Errorf("stale %q kept:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"ENCODER", "_STATISTICS_TAGS", ">new<", "R13"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q missing:\n%s", kept, got)
		}
	}
	if n := strings.Count(got, "<Tag>"); n != 2 {
		t.Errorf("<Tag> count = %d, want 2 (no duplicate block):\n%s", n, got)
	}
}

// End to end through MKVToolNix: repeated applies and a deband-only update
// keep other tags and don't duplicate the ones this tool manages.
func TestRealMKVApplier_TagUpdates_KeepOtherTagsWithoutDuplicates(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	mkvmerge, err := exec.LookPath("mkvmerge")
	if err != nil {
		t.Skip("mkvmerge not in PATH")
	}
	applier, err := newRealMKVApplier()
	if err != nil || applier.extract == "" {
		t.Skip("MKVToolNix not in PATH")
	}
	tagger := newRealMKVTagger()

	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.mkv")
	if out, err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-c:v", "mpeg4", raw).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	// mkvmerge writes _STATISTICS_* tags for every track it muxes.
	movie := filepath.Join(dir, "Film (2020).mkv")
	if out, err := exec.Command(mkvmerge, "-o", movie, raw).CombinedOutput(); err != nil {
		t.Fatalf("mkvmerge: %v\n%s", err, out)
	}

	for _, rating := range []NZRating{RatingM, RatingR16} {
		tagsPath, err := writeTempTagFile(buildMovieTagXML("a < b & c", "42", "2020-01-02", rating, true))
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.Apply(movie, "Film", "2020-01-02", tagsPath, ""); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	if !applyDebandOnlyFile(movie, false, tagger, &fakeFileTimer{}) {
		t.Fatal("applyDebandOnlyFile failed")
	}

	tags, err := tagger.ExtractTags(movie)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ENCODER", "_STATISTICS_TAGS", "a &lt; b &amp; c", "<String>R16</String>"} {
		if !strings.Contains(tags, want) {
			t.Errorf("tags missing %q:\n%s", want, tags)
		}
	}
	for name, want := range map[string]int{"SUMMARY": 1, "LAW_RATING": 1, "DEBAND": 0} {
		if n := strings.Count(tags, "<Name>"+name+"</Name>"); n != want {
			t.Errorf("%s count = %d, want %d:\n%s", name, n, want, tags)
		}
	}
}
