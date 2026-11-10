package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

// legacyXML is what older versions left behind. A season block comes first.
// mkv-movie appended a file-level block on every apply; mkv-tvshow's rating-
// and deband-only updates put COUNTRY and DEBAND in the last block, which
// could be a per-track one.
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

// movieBlock returns the first file-level <Tag> block of xml.
func movieBlock(t *testing.T, xml string) string {
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
	got, err := mergeMovieTags(existing, fresh)
	if err != nil {
		t.Fatalf("mergeMovieTags: %v", err)
	}
	return got
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

func TestUpdateDebandInXML_OnThenOff_DropsEmptiedBlock(t *testing.T) {
	got := updateDebandInXML(updateDebandInXML(trackOnlyXML, true), false)
	if got != trackOnlyXML {
		t.Errorf("got %q, want %q", got, trackOnlyXML)
	}
}

func TestUpdateDebandInXML_LegacyTrackBlockDeband_Removed(t *testing.T) {
	got := updateDebandInXML(legacyXML, false)
	if strings.Contains(got, "DEBAND") || strings.Contains(got, "<TrackUID>12<") {
		t.Errorf("track-block DEBAND or its emptied block kept:\n%s", got)
	}
	assertNoEmptyTag(t, got)
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
	got := mustMerge(t, mkvextractXML, buildMovieTagXML("new", "7", "", RatingR13, false))
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

func TestMergeMovieTags_LegacyBlocks_CleanedAndEmptiedDropped(t *testing.T) {
	got := mustMerge(t, legacyXML, buildMovieTagXML("new", "7", "2020-01-02", RatingR13, false))
	for _, gone := range []string{"old1", "old2", ">M<", "DEBAND", "<TrackUID>12<"} {
		if strings.Contains(got, gone) {
			t.Errorf("stale %q kept:\n%s", gone, got)
		}
	}
	// Fresh elements go into the first file-level block, not the season block.
	if block := movieBlock(t, got); !strings.Contains(block, "ENCODER") || !strings.Contains(block, ">new<") {
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

func TestMergeMovieTags_OnlyTopLevelNZElementsReplaced(t *testing.T) {
	got := mustMerge(t, nestedXML, buildMovieTagXML("", "", "2020-01-02", RatingR13, false))
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

func TestMergeMovieTags_NoFileLevelBlock_AddsOne(t *testing.T) {
	got := mustMerge(t, trackOnlyXML, buildMovieTagXML("s", "", "", RatingNone, false))
	if !strings.Contains(movieBlock(t, got), ">s<") || !strings.Contains(got, "_STATISTICS_TAGS") {
		t.Errorf("want a new file-level block beside the track tags:\n%s", got)
	}
}

func TestMergeMovieTags_EmptyFresh_ReturnsExisting(t *testing.T) {
	if got := mustMerge(t, trackOnlyXML, ""); got != trackOnlyXML {
		t.Errorf("got %q, want existing unchanged", got)
	}
}

func TestMergeMovieTags_ExistingNotTagsDocument_Errors(t *testing.T) {
	if _, err := mergeMovieTags("garbage", buildMovieTagXML("s", "", "", RatingNone, false)); err == nil {
		t.Error("want an error rather than replacing every tag")
	}
}

func TestRealMKVApplier_MergedTagsFile_MergesIntoExtractedTags(t *testing.T) {
	fresh, err := writeTempTagFile(buildMovieTagXML("new", "7", "", RatingNone, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(fresh) })
	a := &realMKVApplier{extractTags: func(string) (string, error) { return mkvextractXML, nil }}

	merged, err := a.mergedTagsFile("film.mkv", fresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(merged) })
	data, err := os.ReadFile(merged)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "_STATISTICS_TAGS") || !strings.Contains(got, ">new<") {
		t.Errorf("merged file doesn't hold existing and fresh tags:\n%s", got)
	}
}

// Applying the fresh tags alone would delete every other tag in the file.
func TestRealMKVApplier_Apply_ExtractFails_ReturnsError(t *testing.T) {
	fresh, err := writeTempTagFile(buildMovieTagXML("new", "7", "", RatingNone, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(fresh) })
	a := &realMKVApplier{
		path:        filepath.Join(t.TempDir(), "never-run"),
		extractTags: func(string) (string, error) { return "", errors.New("mkvextract not found in PATH") },
	}
	if err := a.Apply("film.mkv", "Film", "", fresh, ""); err == nil || !strings.Contains(err.Error(), "mkvextract") {
		t.Errorf("Apply error = %v, want the extraction failure", err)
	}
}

// muxTestMKV writes a one-second MKV to dir via ffmpeg and mkvmerge, which
// adds _STATISTICS_* tags to each track unless told not to. It skips the test
// without ffmpeg or MKVToolNix.
func muxTestMKV(t *testing.T, dir, name string, mkvmergeArgs ...string) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	mkvmerge, err := exec.LookPath("mkvmerge")
	if err != nil {
		t.Skip("mkvmerge not in PATH")
	}
	if !newRealMKVTagger().Available() {
		t.Skip("MKVToolNix not in PATH")
	}
	raw := filepath.Join(dir, "raw-"+name)
	if out, err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-c:v", "mpeg4", raw).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	path := filepath.Join(dir, name)
	args := append(append([]string{"-o", path}, mkvmergeArgs...), raw)
	if out, err := exec.Command(mkvmerge, args...).CombinedOutput(); err != nil {
		t.Fatalf("mkvmerge: %v\n%s", err, out)
	}
	return path
}

func extractRealTags(t *testing.T, path string) string {
	t.Helper()
	tags, err := newRealMKVTagger().ExtractTags(path)
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

func applyMovieTags(t *testing.T, applier *realMKVApplier, movie, xml string) {
	t.Helper()
	tagsPath, err := writeTempTagFile(xml)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(tagsPath) })
	if err := applier.Apply(movie, "Film", "2020-01-02", tagsPath, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// End to end through MKVToolNix: repeated applies and a deband-only update
// keep other tags and don't duplicate the ones this tool manages.
func TestRealMKVApplier_TagUpdates_KeepOtherTagsWithoutDuplicates(t *testing.T) {
	movie := muxTestMKV(t, t.TempDir(), "Film (2020).mkv")
	applier, err := newRealMKVApplier()
	if err != nil {
		t.Fatal(err)
	}

	for _, rating := range []NZRating{RatingM, RatingR16} {
		applyMovieTags(t, applier, movie, buildMovieTagXML("a < b & c", "42", "2020-01-02", rating, true))
	}
	if !applyDebandOnlyFile(movie, false, newRealMKVTagger(), &fakeFileTimer{}) {
		t.Fatal("applyDebandOnlyFile failed")
	}

	tags := extractRealTags(t, movie)
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
func TestRealMKVApplier_LegacyDuplicateBlocks_ApplySucceeds(t *testing.T) {
	dir := t.TempDir()
	movie := muxTestMKV(t, dir, "Film (2020).mkv")
	legacy := `<Tags>` +
		`<Tag><Targets /><Simple><Name>SUMMARY</Name><String>old1</String></Simple><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag>` +
		`<Tag><Targets /><Simple><Name>SUMMARY</Name><String>old2</String></Simple><Simple><Name>TMDB_ID</Name><String>1</String></Simple></Tag>` +
		`</Tags>`
	legacyPath := filepath.Join(dir, "legacy.xml")
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	tagger := newRealMKVTagger()
	if err := tagger.ApplyTags(movie, legacyPath); err != nil {
		t.Fatalf("writing legacy tags: %v", err)
	}

	applier, err := newRealMKVApplier()
	if err != nil {
		t.Fatal(err)
	}
	applyMovieTags(t, applier, movie, buildMovieTagXML("new", "42", "", RatingNone, false))
	tags := extractRealTags(t, movie)
	assertNameCounts(t, tags, map[string]int{"SUMMARY": 1, "TMDB_ID": 1, "DEBAND": 0})
	if n := strings.Count(tags, "<Tag>"); n != 1 {
		t.Errorf("<Tag> count = %d, want 1:\n%s", n, tags)
	}
}

// Deband on then off leaves no empty file-level block on a file whose only
// other tags are per-track, and deband off on an untagged file succeeds.
func TestRealMKVTagger_DebandOnOff_NoEmptyBlock(t *testing.T) {
	dir := t.TempDir()
	statsOnly := muxTestMKV(t, dir, "stats.mkv", "--no-global-tags")
	untagged := muxTestMKV(t, dir, "untagged.mkv", "--no-global-tags", "--disable-track-statistics-tags")
	tagger := newRealMKVTagger()

	if !applyDebandOnlyFile(statsOnly, true, tagger, &fakeFileTimer{}) || !applyDebandOnlyFile(statsOnly, false, tagger, &fakeFileTimer{}) {
		t.Fatal("applyDebandOnlyFile on a stats-only file failed")
	}
	assertNameCounts(t, extractRealTags(t, statsOnly), map[string]int{"DEBAND": 0, "_STATISTICS_TAGS": 1})

	if !applyDebandOnlyFile(untagged, false, tagger, &fakeFileTimer{}) {
		t.Fatal("applyDebandOnlyFile on an untagged file failed")
	}
}
