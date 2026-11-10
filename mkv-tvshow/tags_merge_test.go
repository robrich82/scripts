package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// trackStatsXML has a file-level block followed by a per-track statistics block.
const trackStatsXML = `<?xml version="1.0"?>
<Tags>
  <Tag>
    <Targets>
      <TargetTypeValue>50</TargetTypeValue>
    </Targets>
    <Simple>
      <Name>SUMMARY</Name>
      <String>old</String>
    </Simple>
  </Tag>
  <Tag>
    <Targets>
      <TrackUID>123</TrackUID>
    </Targets>
    <Simple>
      <Name>_STATISTICS_TAGS</Name>
      <String>BPS</String>
    </Simple>
  </Tag>
</Tags>`

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

// episodeBlock returns the first file-level <Tag> block of xml.
func episodeBlock(t *testing.T, xml string) string {
	t.Helper()
	for _, loc := range tagBlockRe.FindAllStringIndex(xml, -1) {
		if block := xml[loc[0]:loc[1]]; isFileLevelTag(block) {
			return block
		}
	}
	t.Fatalf("no file-level block in %q", xml)
	return ""
}

func TestUpdateDebandInXML_InsertsIntoFileLevelBlockNotTrackBlock(t *testing.T) {
	got := updateDebandInXML(trackStatsXML, true)
	if !strings.Contains(episodeBlock(t, got), "DEBAND") {
		t.Errorf("DEBAND not in the file-level block:\n%s", got)
	}
	if strings.Count(got, "DEBAND") != 1 {
		t.Errorf("want exactly one DEBAND element:\n%s", got)
	}
}

func TestUpdateRatingInXML_InsertsIntoFileLevelBlockNotTrackBlock(t *testing.T) {
	got := updateRatingInXML(trackStatsXML, RatingR16)
	if !strings.Contains(episodeBlock(t, got), "LAW_RATING") {
		t.Errorf("LAW_RATING not in the file-level block:\n%s", got)
	}
}

func TestUpdateRatingInXML_OnlyTrackTags_AddsFileLevelBlock(t *testing.T) {
	trackOnly := `<Tags><Tag><Targets><TrackUID>1</TrackUID></Targets><Simple><Name>_STATISTICS_TAGS</Name><String>BPS</String></Simple></Tag></Tags>`
	got := updateRatingInXML(trackOnly, RatingM)
	if !strings.Contains(episodeBlock(t, got), "LAW_RATING") {
		t.Errorf("LAW_RATING not in a new file-level block:\n%s", got)
	}
	if !strings.Contains(got, "_STATISTICS_TAGS") {
		t.Errorf("track tags dropped:\n%s", got)
	}
}

func TestUpdateDebandInXML_MkvextractOutput_RemovesDeband(t *testing.T) {
	if got := updateDebandInXML(mkvextractXML, false); strings.Contains(got, "DEBAND") {
		t.Errorf("DEBAND not removed:\n%s", got)
	}
}

func TestUpdateRatingInXML_MkvextractOutput_ReplacesRating(t *testing.T) {
	got := updateRatingInXML(mkvextractXML, RatingR16)
	if n := strings.Count(got, "LAW_RATING"); n != 1 {
		t.Errorf("LAW_RATING count = %d, want 1:\n%s", n, got)
	}
	if !strings.Contains(episodeBlock(t, got), "R16") {
		t.Errorf("new rating not in the file-level block:\n%s", got)
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

func TestMergeEpisodeTags_ReplacesManagedKeepsTrackTags(t *testing.T) {
	got := mergeEpisodeTags(trackStatsXML, buildTagXML("new summary", "42", "", RatingNone, false))
	if strings.Contains(got, ">old<") {
		t.Errorf("old summary kept:\n%s", got)
	}
	if !strings.Contains(got, "new summary") || !strings.Contains(got, "_STATISTICS_TAGS") {
		t.Errorf("merge lost fresh or track tags:\n%s", got)
	}
}

func TestMergeEpisodeTags_MkvextractOutput_ReplacesManagedKeepsOthers(t *testing.T) {
	got := mergeEpisodeTags(mkvextractXML, buildTagXML("new", "7", "", RatingR13, false))
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

func TestMergeEpisodeTags_NoExistingTags_ReturnsFresh(t *testing.T) {
	fresh := buildTagXML("s", "", "", RatingNone, false)
	if got := mergeEpisodeTags("", fresh); got != fresh {
		t.Errorf("got %q, want %q", got, fresh)
	}
}

func TestBuildTagXML_EscapesSummary(t *testing.T) {
	got := buildTagXML(`Tom & Jerry <meet> "again"`, "", "", RatingNone, false)
	if !strings.Contains(got, "Tom &amp; Jerry &lt;meet&gt; &quot;again&quot;") {
		t.Errorf("summary not escaped: %s", got)
	}
}

func TestApplyMKV_MergesIntoExistingTags(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml", extractTagsXML: trackStatsXML}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "T", "fresh", "1", "", "", "", RatingNone, false)

	if len(runner.writtenXML) != 1 {
		t.Fatalf("WriteTempTags calls = %d, want 1", len(runner.writtenXML))
	}
	if xml := runner.writtenXML[0]; !strings.Contains(xml, "_STATISTICS_TAGS") || !strings.Contains(xml, "fresh") {
		t.Errorf("written tags don't merge existing and fresh:\n%s", xml)
	}
}

func TestApplyMKV_ExtractFails_AppliesFreshTags(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml", extractTagsErr: os.ErrNotExist}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	if !p.applyMKV("show.mkv", "T", "fresh", "1", "", "", "", RatingNone, false) {
		t.Fatal("applyMKV failed")
	}
	if want := buildTagXML("fresh", "1", "", RatingNone, false); runner.writtenXML[0] != want {
		t.Errorf("got %q, want %q", runner.writtenXML[0], want)
	}
}

// End to end through MKVToolNix: repeated full applies, a rating-only update
// and a deband-only update keep other tags, don't duplicate managed ones, and
// survive a summary containing XML special characters.
func TestRealMKVRunner_TagUpdates_KeepOtherTagsWithoutDuplicates(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	runner := newRealMKVRunner()
	if !runner.Available() || runner.mkvmerge == "" || runner.mkvextract == "" {
		t.Skip("MKVToolNix not in PATH")
	}
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.mkv")
	if out, err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-c:v", "mpeg4", raw).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	// mkvmerge writes _STATISTICS_* tags for every track it muxes.
	ep := filepath.Join(dir, "Show - S01E01.mkv")
	if out, err := exec.Command(runner.mkvmerge, "-o", ep, raw).CombinedOutput(); err != nil {
		t.Fatalf("mkvmerge: %v\n%s", err, out)
	}

	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	for range 2 {
		if !p.applyMKV(ep, "Pilot", "a < b & c", "42", "2020-01-02T00:00:00Z", "", "", RatingM, true) {
			t.Fatal("applyMKV failed")
		}
	}
	if !p.applyRatingOnlyMKV(ep, RatingR16) {
		t.Fatal("applyRatingOnlyMKV failed")
	}
	if !p.applyDebandOnlyMKV(ep, false) {
		t.Fatal("applyDebandOnlyMKV failed")
	}

	tags, err := runner.ExtractTags(ep)
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
