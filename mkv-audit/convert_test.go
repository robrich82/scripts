package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pathIdentifier returns tracks by file extension: the source extension maps
// to srcTracks, anything else (the converted temp file) to outTracks.
type pathIdentifier struct {
	srcExt    string
	srcTracks int
	outTracks int
	srcErr    error
}

func (p *pathIdentifier) Identify(path string) (*MKVInfo, error) {
	n := p.outTracks
	if strings.EqualFold(filepath.Ext(path), p.srcExt) {
		if p.srcErr != nil {
			return nil, p.srcErr
		}
		n = p.srcTracks
	}
	info := &MKVInfo{}
	for i := range n {
		info.Tracks = append(info.Tracks, Track{ID: i, Type: "video"})
	}
	return info, nil
}

func (p *pathIdentifier) ReadTagDate(string) (time.Time, bool) { return time.Time{}, false }

// writingMux stands in for mkvmerge: it writes dst, or returns err.
func writingMux(err error) func(src, dst string) error {
	return func(_, dst string) error {
		if err != nil {
			return err
		}
		return os.WriteFile(dst, []byte("mkv"), 0o644)
	}
}

// oldSource writes dir/name with a modification time in the past.
func oldSource(t *testing.T, dir, name string) (string, time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := time.Date(2019, 6, 1, 12, 0, 0, 0, time.Local)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path, mod
}

// assertOnly fails unless dir holds exactly the named files.
func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Errorf("dir holds %v, want %v", got, names)
	}
}

func TestConvertToMKV_Success(t *testing.T) {
	dir := t.TempDir()
	src, mod := oldSource(t, dir, "Movie (2019).mp4")

	dst, err := convertToMKV(src, &pathIdentifier{srcExt: ".mp4", srcTracks: 2, outTracks: 2}, writingMux(nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "Movie (2019).mkv"); dst != want {
		t.Errorf("dst = %s, want %s", dst, want)
	}
	assertOnly(t, dir, "Movie (2019).mkv")
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(mod) {
		t.Errorf("mtime = %v, want the source's %v", st.ModTime(), mod)
	}
}

func TestConvertToMKV_MKVExists_LeavesBoth(t *testing.T) {
	dir := t.TempDir()
	src, _ := oldSource(t, dir, "ep.avi")
	if err := os.WriteFile(filepath.Join(dir, "ep.mkv"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	muxed := false
	mux := func(string, string) error { muxed = true; return nil }

	_, err := convertToMKV(src, &pathIdentifier{srcExt: ".avi", srcTracks: 1, outTracks: 1}, mux)
	if !errors.Is(err, errMKVExists) {
		t.Errorf("err = %v, want errMKVExists", err)
	}
	if muxed {
		t.Error("mkvmerge ran although the MKV exists")
	}
	assertOnly(t, dir, "ep.avi", "ep.mkv")
}

func TestConvertToMKV_UnsupportedContainer(t *testing.T) {
	for name, ident := range map[string]*pathIdentifier{
		"no tracks":      {srcExt: ".wmv"},
		"identify error": {srcExt: ".wmv", srcErr: errors.New("exit status 2")},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src, _ := oldSource(t, dir, "clip.wmv")
			_, err := convertToMKV(src, ident, writingMux(nil))
			if !errors.Is(err, errUnsupportedContainer) {
				t.Errorf("err = %v, want errUnsupportedContainer", err)
			}
			assertOnly(t, dir, "clip.wmv")
		})
	}
}

func TestConvertToMKV_MuxFails_KeepsSource(t *testing.T) {
	dir := t.TempDir()
	src, _ := oldSource(t, dir, "ep.mpg")
	partial := func(_, dst string) error {
		_ = os.WriteFile(dst, []byte("half"), 0o644)
		return errors.New("mkvmerge: exit status 2")
	}

	if _, err := convertToMKV(src, &pathIdentifier{srcExt: ".mpg", srcTracks: 2, outTracks: 2}, partial); err == nil {
		t.Fatal("want an error")
	}
	assertOnly(t, dir, "ep.mpg")
}

func TestConvertToMKV_TrackCountMismatch_KeepsSource(t *testing.T) {
	dir := t.TempDir()
	src, _ := oldSource(t, dir, "ep.mov")

	_, err := convertToMKV(src, &pathIdentifier{srcExt: ".mov", srcTracks: 3, outTracks: 2}, writingMux(nil))
	if err == nil || !strings.Contains(err.Error(), "2 tracks, source has 3") {
		t.Errorf("err = %v, want a track count mismatch", err)
	}
	assertOnly(t, dir, "ep.mov")
}

// End to end through mkvmerge: a real MP4 becomes an MKV with the same tracks
// and timestamps, and an unsupported WMV is left alone.
func TestRealMKVRunner_ConvertToMKV(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	runner, err := newRealMKVRunner()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	makeClip := func(name, vcodec, acodec string) string {
		path := filepath.Join(dir, name)
		out, err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
			"-f", "lavfi", "-i", "sine=duration=1", "-c:v", vcodec, "-c:a", acodec, "-shortest", path).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg %s: %v\n%s", name, err, out)
		}
		return path
	}
	mp4 := makeClip("clip.mp4", "mpeg4", "aac")
	mod := time.Date(2019, 6, 1, 12, 0, 0, 0, time.Local)
	if err := os.Chtimes(mp4, mod, mod); err != nil {
		t.Fatal(err)
	}
	srcStat, err := os.Stat(mp4)
	if err != nil {
		t.Fatal(err)
	}

	dst, err := runner.ConvertToMKV(mp4)
	if err != nil {
		t.Fatal(err)
	}
	info, err := runner.Identify(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Tracks) != 2 {
		t.Errorf("MKV has %d tracks, want 2", len(info.Tracks))
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(mod) {
		t.Errorf("mtime = %v, want %v", st.ModTime(), mod)
	}
	if ct, want := creationTime(st), creationTime(srcStat); !ct.Equal(want) {
		t.Errorf("creation time = %v, want the source's %v", ct, want)
	}

	wmv := makeClip("other.wmv", "wmv2", "wmav2")
	if _, err := runner.ConvertToMKV(wmv); !errors.Is(err, errUnsupportedContainer) {
		t.Errorf("wmv: err = %v, want errUnsupportedContainer", err)
	}
	assertOnly(t, dir, "clip.mkv", "other.wmv")
}
