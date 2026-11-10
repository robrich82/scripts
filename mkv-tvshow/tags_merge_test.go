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

// trackOnlyXML has only a per-track statistics block.
const trackOnlyXML = `<Tags><Tag><Targets><TrackUID>1</TrackUID></Targets><Simple><Name>_STATISTICS_TAGS</Name><String>BPS</String></Simple></Tag></Tags>`

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

// legacyXML is what older versions left behind, with a season block first.
const legacyXML = `<Tags>` +
	`<Tag><Targets><TargetTypeValue>60</TargetTypeValue></Targets><Simple><Name>DATE</Name><String>2019</String></Simple></Tag>` +
	`<Tag><Targets /><Simple><Name>ENCODER</Name><String>Lavf</String></Simple><Simple><Name>SUMMARY</Name><String>old1</String></Simple></Tag>` +
	`<Tag><Targets><TrackUID>11</TrackUID></Targets><Simple><Name>_STATISTICS_TAGS</Name><String>BPS</String></Simple>` +
	`<Simple><Name>COUNTRY</Name><String>NZ</String><Simple><Name>LAW_RATING</Name><String>M</String></Simple></Simple></Tag>` +
	`<Tag><Targets /><Simple><Name>SUMMARY</Name><String>old2</String></Simple><Simple><Name>TMDB_ID</Name><String>1</String></Simple></Tag>` +
	`<Tag><Targets><TrackUID>12</TrackUID></Targets><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag>` +
	`</Tags>`

// nestedXML has elements another tool wrote: a DATE nested under ALBUM, a US
// rating, and an NZ COUNTRY carrying a child besides LAW_RATING.
const nestedXML = `<Tags><Tag><Targets />` +
	`<Simple><Name>ALBUM</Name><String>x</String><Simple><Name>DATE</Name><String>1990</String></Simple></Simple>` +
	`<Simple><Name>COUNTRY</Name><String>US</String><Simple><Name>LAW_RATING</Name><String>PG</String></Simple></Simple>` +
	`<Simple><Name>COUNTRY</Name><String>NZ</String><Simple><Name>OTHER</Name><String>y</String></Simple>` +
	`<Simple><Name>LAW_RATING</Name><String>M</String></Simple></Simple>` +
	`</Tag></Tags>`

// fileLevelBlock returns the first file-level <Tag> block of xml.
func fileLevelBlock(t *testing.T, xml string) string {
	t.Helper()
	for _, loc := range tagBlockRe.FindAllStringIndex(xml, -1) {
		if block := xml[loc[0]:loc[1]]; isFileLevelTag(block) {
			return block
		}
	}
	t.Fatalf("no file-level block in %q", xml)
	return ""
}

func mustMerge(t *testing.T, existing, fresh string) string {
	t.Helper()
	got, err := mergeEpisodeTags(existing, fresh)
	if err != nil {
		t.Fatalf("mergeEpisodeTags: %v", err)
	}
	return got
}

func debandXML(t *testing.T, in string, deband bool) string {
	t.Helper()
	got, err := updateDebandInXML(in, deband)
	if err != nil {
		t.Fatalf("updateDebandInXML: %v", err)
	}
	return got
}

func ratingXML(t *testing.T, in string, rating NZRating) string {
	t.Helper()
	got, err := updateRatingInXML(in, rating)
	if err != nil {
		t.Fatalf("updateRatingInXML: %v", err)
	}
	return got
}

// countryAfterRatingXML is an NZ rating with <String> after the nested
// LAW_RATING, an order other tools may write and mkvextract keeps.
const countryAfterRatingXML = `<Tags><Tag><Targets /><Simple><Name>COUNTRY</Name>` +
	`<Simple><Name>LAW_RATING</Name><String>M</String></Simple><String>NZ</String></Simple></Tag></Tags>`

// notTagsDocs are inputs mkvpropedit would apply as "no tags" (<Tags/>) or
// reject (truncated), so the tag edits refuse them.
var notTagsDocs = []string{
	"<Tags/>",
	"<!-- </Tags> --><Tags/>",
	"<Tags/><!-- </Tags> -->",
	`<Tags><Tag><Targets /><Simple><Name>X</Name><String>1</String></Simple></Tag>`,
	"garbage",
}

func TestUpdateRatingInXML_StringAfterNestedRating_Replaced(t *testing.T) {
	got := ratingXML(t, countryAfterRatingXML, RatingR16)
	if n := strings.Count(got, "LAW_RATING"); n != 1 || strings.Contains(got, ">M<") {
		t.Errorf("old rating kept beside the new one:\n%s", got)
	}
}

func TestUpdateRatingInXML_NZCountryWithoutRating_Kept(t *testing.T) {
	in := `<Tags><Tag><Targets /><Simple><Name>COUNTRY</Name><String>NZ</String></Simple></Tag></Tags>`
	if n := strings.Count(ratingXML(t, in, RatingR16), "<Name>COUNTRY</Name>"); n != 2 {
		t.Errorf("COUNTRY count = %d, want 2 (another tool's NZ country kept)", n)
	}
}

func TestTagEdits_NotTagsDocument_Error(t *testing.T) {
	fresh := buildTagXML("s", "", "", RatingM, false)
	for _, in := range notTagsDocs {
		if _, err := updateDebandInXML(in, true); err == nil {
			t.Errorf("updateDebandInXML(%q): want error", in)
		}
		if _, err := updateRatingInXML(in, RatingM); err == nil {
			t.Errorf("updateRatingInXML(%q): want error", in)
		}
		if _, err := mergeEpisodeTags(in, fresh); err == nil {
			t.Errorf("mergeEpisodeTags(%q): want error", in)
		}
	}
}

// The error quotes the tail: mkvextract's header fills the start.
func TestCheckTagsDoc_ErrorQuotesTail(t *testing.T) {
	in := "<?xml version=\"1.0\"?>" + strings.Repeat(" ", 100) + "<Tags><Tag>truncated"
	err := checkTagsDoc(in)
	if err == nil || !strings.Contains(err.Error(), "truncated") || strings.Contains(err.Error(), "xml version") {
		t.Errorf("error = %v, want the tail quoted", err)
	}
}

// A block whose <Simple> elements the scanner can't see isn't one this tool
// emptied, so it is left alone.
func TestUpdateDebandInXML_UnrecognisedBlock_Kept(t *testing.T) {
	in := `<Tags><Tag><Targets /><Simple lang="x"><Name>X</Name></Simple></Tag></Tags>`
	if got := debandXML(t, in, false); got != in {
		t.Errorf("got %q, want input unchanged", got)
	}
}

func TestMergeEpisodeTags_FreshWithoutTag_Errors(t *testing.T) {
	if _, err := mergeEpisodeTags(trackStatsXML, emptyTagsDoc); err == nil {
		t.Error("want an error for fresh tags with no <Tag> block")
	}
}

// assertNoEmptyTag fails if any <Tag> has no top-level <Simple>, which
// mkvpropedit rejects.
func assertNoEmptyTag(t *testing.T, xml string) {
	t.Helper()
	for _, block := range tagBlockRe.FindAllString(xml, -1) {
		if len(topLevelSimples(block)) == 0 {
			t.Errorf("empty <Tag> block %q in:\n%s", block, xml)
		}
	}
}

func TestUpdateDebandInXML_InsertsIntoFileLevelBlockNotTrackBlock(t *testing.T) {
	got := debandXML(t, trackStatsXML, true)
	if !strings.Contains(fileLevelBlock(t, got), "DEBAND") {
		t.Errorf("DEBAND not in the file-level block:\n%s", got)
	}
	if strings.Count(got, "DEBAND") != 1 {
		t.Errorf("want exactly one DEBAND element:\n%s", got)
	}
}

func TestUpdateDebandInXML_OnThenOff_DropsEmptiedBlock(t *testing.T) {
	got := debandXML(t, debandXML(t, trackOnlyXML, true), false)
	if got != trackOnlyXML {
		t.Errorf("got %q, want %q", got, trackOnlyXML)
	}
}

func TestUpdateRatingInXML_InsertsIntoFileLevelBlockNotTrackBlock(t *testing.T) {
	got := ratingXML(t, trackStatsXML, RatingR16)
	if !strings.Contains(fileLevelBlock(t, got), "LAW_RATING") {
		t.Errorf("LAW_RATING not in the file-level block:\n%s", got)
	}
}

func TestUpdateRatingInXML_OnlyTrackTags_AddsFileLevelBlock(t *testing.T) {
	got := ratingXML(t, trackOnlyXML, RatingM)
	if !strings.Contains(fileLevelBlock(t, got), "LAW_RATING") {
		t.Errorf("LAW_RATING not in a new file-level block:\n%s", got)
	}
	if !strings.Contains(got, "_STATISTICS_TAGS") {
		t.Errorf("track tags dropped:\n%s", got)
	}
}

func TestUpdateRatingInXML_RemovingOnlyTag_LeavesEmptyTagsDoc(t *testing.T) {
	only := `<Tags><Tag><Targets /><Simple><Name>COUNTRY</Name><String>NZ</String>` +
		`<Simple><Name>LAW_RATING</Name><String>M</String></Simple></Simple></Tag></Tags>`
	if got := ratingXML(t, only, RatingNone); got != emptyTagsDoc {
		t.Errorf("got %q, want %q", got, emptyTagsDoc)
	}
}

func TestUpdateRatingInXML_LegacyTrackBlockRating_Moved(t *testing.T) {
	got := ratingXML(t, legacyXML, RatingR16)
	if n := strings.Count(got, "LAW_RATING"); n != 1 {
		t.Errorf("LAW_RATING count = %d, want 1:\n%s", n, got)
	}
	if !strings.Contains(fileLevelBlock(t, got), "R16") {
		t.Errorf("new rating not in the file-level block:\n%s", got)
	}
}

func TestUpdateRatingInXML_KeepsOtherCountries(t *testing.T) {
	got := ratingXML(t, nestedXML, RatingR13)
	for _, want := range []string{">PG<", ">1990<", ">R13<"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing:\n%s", want, got)
		}
	}
	if strings.Contains(got, ">M<") {
		t.Errorf("old NZ rating kept:\n%s", got)
	}
}

func TestUpdateDebandInXML_MkvextractOutput_RemovesDeband(t *testing.T) {
	if got := debandXML(t, mkvextractXML, false); strings.Contains(got, "DEBAND") {
		t.Errorf("DEBAND not removed:\n%s", got)
	}
}

func TestUpdateRatingInXML_MkvextractOutput_ReplacesRating(t *testing.T) {
	got := ratingXML(t, mkvextractXML, RatingR16)
	if n := strings.Count(got, "LAW_RATING"); n != 1 {
		t.Errorf("LAW_RATING count = %d, want 1:\n%s", n, got)
	}
	if !strings.Contains(fileLevelBlock(t, got), "R16") {
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
	got := mustMerge(t, trackStatsXML, buildTagXML("new summary", "42", "", RatingNone, false))
	if strings.Contains(got, ">old<") {
		t.Errorf("old summary kept:\n%s", got)
	}
	if !strings.Contains(got, "new summary") || !strings.Contains(got, "_STATISTICS_TAGS") {
		t.Errorf("merge lost fresh or track tags:\n%s", got)
	}
}

func TestMergeEpisodeTags_MkvextractOutput_ReplacesManagedKeepsOthers(t *testing.T) {
	got := mustMerge(t, mkvextractXML, buildTagXML("new", "7", "", RatingR13, false))
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

func TestMergeEpisodeTags_LegacyBlocks_CleanedAndEmptiedDropped(t *testing.T) {
	got := mustMerge(t, legacyXML, buildTagXML("new", "7", "2020-01-02", RatingR13, false))
	for _, gone := range []string{"old1", "old2", ">M<", "DEBAND", "<TrackUID>12<"} {
		if strings.Contains(got, gone) {
			t.Errorf("stale %q kept:\n%s", gone, got)
		}
	}
	// Fresh elements go into the first file-level block, not the season block.
	if block := fileLevelBlock(t, got); !strings.Contains(block, "ENCODER") || !strings.Contains(block, ">new<") {
		t.Errorf("fresh tags not merged into the ENCODER block:\n%s", got)
	}
	for name, want := range map[string]int{"SUMMARY": 1, "TMDB_ID": 1, "YEAR": 1, "DATE": 2, "LAW_RATING": 1, "_STATISTICS_TAGS": 1} {
		if n := strings.Count(got, "<Name>"+name+"</Name>"); n != want {
			t.Errorf("%s count = %d, want %d:\n%s", name, n, want, got)
		}
	}
	if !strings.Contains(got, ">2019<") {
		t.Errorf("season DATE dropped:\n%s", got)
	}
	if n := strings.Count(got, "<Tag>"); n != 3 {
		t.Errorf("<Tag> count = %d, want 3 (season, file, track 11):\n%s", n, got)
	}
	assertNoEmptyTag(t, got)
}

func TestMergeEpisodeTags_OnlyTopLevelNZElementsReplaced(t *testing.T) {
	got := mustMerge(t, nestedXML, buildTagXML("", "", "2020-01-02", RatingR13, false))
	for _, kept := range []string{">1990<", ">PG<", ">R13<", ">2020-01-02<"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q missing:\n%s", kept, got)
		}
	}
	for _, gone := range []string{">M<", "OTHER"} {
		if strings.Contains(got, gone) {
			t.Errorf("stale %q kept:\n%s", gone, got)
		}
	}
}

func TestMergeEpisodeTags_NoFileLevelBlock_AddsOne(t *testing.T) {
	got := mustMerge(t, trackOnlyXML, buildTagXML("s", "", "", RatingNone, false))
	if !strings.Contains(fileLevelBlock(t, got), ">s<") || !strings.Contains(got, "_STATISTICS_TAGS") {
		t.Errorf("want a new file-level block beside the track tags:\n%s", got)
	}
}

func TestMergeEpisodeTags_NoExistingTags_ReturnsFresh(t *testing.T) {
	fresh := buildTagXML("s", "", "", RatingNone, false)
	if got := mustMerge(t, "", fresh); got != fresh {
		t.Errorf("got %q, want %q", got, fresh)
	}
}

func TestMergeEpisodeTags_EmptyFresh_ReturnsExisting(t *testing.T) {
	if got := mustMerge(t, trackStatsXML, ""); got != trackStatsXML {
		t.Errorf("got %q, want existing unchanged", got)
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

// Applying the fresh tags alone would delete every other tag in the file.
func TestApplyMKV_ExtractFails_DoesNotApply(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml", extractTagsErr: os.ErrNotExist}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	if p.applyMKV("show.mkv", "T", "fresh", "1", "", "", "", RatingNone, false) {
		t.Error("applyMKV succeeded, want failure")
	}
	if len(runner.calls) != 0 {
		t.Errorf("mkvpropedit ran %d times, want 0", len(runner.calls))
	}
}

func TestApplyMKV_MergeFails_DoesNotApply(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml", extractTagsXML: "<Tags/>"}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	if p.applyMKV("show.mkv", "T", "fresh", "1", "", "", "", RatingNone, false) {
		t.Error("applyMKV succeeded, want failure")
	}
	if len(runner.calls) != 0 {
		t.Errorf("mkvpropedit ran %d times, want 0", len(runner.calls))
	}
}

func TestApplyDebandOnlyMKV_EmptiedBlock_WritesEmptyTagsDoc(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml",
		extractTagsXML: `<Tags><Tag><Targets /><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag></Tags>`}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	if !p.applyDebandOnlyMKV(debandTestFile(t), false) {
		t.Fatal("applyDebandOnlyMKV failed")
	}
	if len(runner.writtenXML) != 1 || runner.writtenXML[0] != emptyTagsDoc {
		t.Errorf("written tags = %q, want [%q]", runner.writtenXML, emptyTagsDoc)
	}
}

func TestApplyDebandOnlyMKV_NotTagsDocument_DoesNotApply(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml", extractTagsXML: "<Tags/>"}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	if p.applyDebandOnlyMKV(debandTestFile(t), true) {
		t.Error("applyDebandOnlyMKV succeeded, want failure")
	}
	if len(runner.calls) != 0 {
		t.Errorf("mkvpropedit ran %d times, want 0", len(runner.calls))
	}
}

func TestApplyRatingOnlyMKV_NotTagsDocument_DoesNotApply(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml", extractTagsXML: "<Tags/>"}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	if p.applyRatingOnlyMKV(debandTestFile(t), RatingM) {
		t.Error("applyRatingOnlyMKV succeeded, want failure")
	}
	if len(runner.calls) != 0 {
		t.Errorf("mkvpropedit ran %d times, want 0", len(runner.calls))
	}
}

func TestApplyMKV_WriteTempTagsFails_DoesNotApply(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsErr: os.ErrPermission}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	if p.applyMKV("show.mkv", "T", "fresh", "1", "", "", "", RatingNone, false) {
		t.Error("applyMKV succeeded, want failure")
	}
	if len(runner.calls) != 0 {
		t.Errorf("mkvpropedit ran %d times, want 0", len(runner.calls))
	}
}

// muxTestMKV writes a one-second MKV to dir via ffmpeg and mkvmerge, which
// adds _STATISTICS_* tags to each track unless told not to. It skips the test
// without ffmpeg or MKVToolNix.
func muxTestMKV(t *testing.T, dir, name string, mkvmergeArgs ...string) (string, *realMKVRunner) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	runner := newRealMKVRunner()
	if !runner.Available() || runner.mkvmerge == "" || runner.mkvextract == "" {
		t.Skip("MKVToolNix not in PATH")
	}
	raw := filepath.Join(dir, "raw-"+name)
	if out, err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-c:v", "mpeg4", raw).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	path := filepath.Join(dir, name)
	args := append(append([]string{"-o", path}, mkvmergeArgs...), raw)
	if out, err := exec.Command(runner.mkvmerge, args...).CombinedOutput(); err != nil {
		t.Fatalf("mkvmerge: %v\n%s", err, out)
	}
	return path, runner
}

func extractRealTags(t *testing.T, runner *realMKVRunner, path string) string {
	t.Helper()
	tags, err := runner.ExtractTags(path)
	if err != nil {
		t.Fatal(err)
	}
	return tags
}

func assertNameCounts(t *testing.T, tags string, want map[string]int) {
	t.Helper()
	for name, n := range want {
		if got := strings.Count(tags, "<Name>"+name+"</Name>"); got != n {
			t.Errorf("%s count = %d, want %d:\n%s", name, got, n, tags)
		}
	}
}

// End to end through MKVToolNix: repeated full applies, a rating-only update
// and a deband-only update keep other tags, don't duplicate managed ones, and
// survive a summary containing XML special characters.
func TestRealMKVRunner_TagUpdates_KeepOtherTagsWithoutDuplicates(t *testing.T) {
	ep, runner := muxTestMKV(t, t.TempDir(), "Show - S01E01.mkv")
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

	tags := extractRealTags(t, runner, ep)
	for _, want := range []string{"ENCODER", "_STATISTICS_TAGS", "a &lt; b &amp; c", "<String>R16</String>"} {
		if !strings.Contains(tags, want) {
			t.Errorf("tags missing %q:\n%s", want, tags)
		}
	}
	assertNameCounts(t, tags, map[string]int{"SUMMARY": 1, "DATE": 1, "YEAR": 1, "TMDB_ID": 1, "LAW_RATING": 1, "DEBAND": 0})
}

// A file carrying the duplicate file-level blocks older versions wrote still
// takes a full apply: the emptied blocks are dropped, not left for
// mkvpropedit to reject.
func TestRealMKVRunner_LegacyDuplicateBlocks_ApplySucceeds(t *testing.T) {
	dir := t.TempDir()
	ep, runner := muxTestMKV(t, dir, "Show - S01E01.mkv")
	legacy := `<Tags>` +
		`<Tag><Targets /><Simple><Name>SUMMARY</Name><String>old1</String></Simple><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag>` +
		`<Tag><Targets /><Simple><Name>SUMMARY</Name><String>old2</String></Simple><Simple><Name>TMDB_ID</Name><String>1</String></Simple></Tag>` +
		`</Tags>`
	legacyPath := filepath.Join(dir, "legacy.xml")
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, rc, err := runner.Apply(ep, []string{"--tags", "all:" + legacyPath}); err != nil || rc != 0 {
		t.Fatalf("writing legacy tags: rc=%d err=%v\n%s", rc, err, out)
	}

	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	if !p.applyMKV(ep, "Pilot", "new", "42", "", "", "", RatingNone, false) {
		t.Fatal("applyMKV failed")
	}
	tags := extractRealTags(t, runner, ep)
	assertNameCounts(t, tags, map[string]int{"SUMMARY": 1, "TMDB_ID": 1, "DEBAND": 0})
	if n := strings.Count(tags, "<Tag>"); n != 1 {
		t.Errorf("<Tag> count = %d, want 1:\n%s", n, tags)
	}
}

// Deband on then off leaves no empty file-level block on a file whose only
// other tags are per-track, and deband off on an untagged file succeeds.
func TestRealMKVRunner_DebandOnOff_NoEmptyBlock(t *testing.T) {
	dir := t.TempDir()
	statsOnly, runner := muxTestMKV(t, dir, "stats.mkv", "--no-global-tags")
	untagged, _ := muxTestMKV(t, dir, "untagged.mkv", "--no-global-tags", "--disable-track-statistics-tags", "-T")
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}

	if !p.applyDebandOnlyMKV(statsOnly, true) || !p.applyDebandOnlyMKV(statsOnly, false) {
		t.Fatal("applyDebandOnlyMKV on a stats-only file failed")
	}
	tags := extractRealTags(t, runner, statsOnly)
	assertNameCounts(t, tags, map[string]int{"DEBAND": 0, "_STATISTICS_TAGS": 1})

	if !p.applyDebandOnlyMKV(untagged, false) {
		t.Fatal("applyDebandOnlyMKV on an untagged file failed")
	}
}
