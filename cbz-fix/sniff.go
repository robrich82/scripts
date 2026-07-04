package main

import (
	"bytes"
	"io"
	"os"
)

// mismatchedArchive describes a .cbz file whose contents are actually a
// different archive format. correctExt is the comic-archive extension the
// file should carry (e.g. ".cbr"); format is a human-readable name for
// messages (e.g. "RAR").
type mismatchedArchive struct {
	correctExt string
	format     string
}

// archiveSignature maps a recognised non-zip archive format to the comic
// extension its container should use.
type archiveSignature struct {
	format     string
	correctExt string
	// match reports whether buf (a file's leading bytes) is this format.
	match func(buf []byte) bool
}

var archiveSignatures = []archiveSignature{
	{
		format:     "RAR",
		correctExt: ".cbr",
		// RAR 4.x: "Rar!\x1a\x07\x00"; RAR 5.x: "Rar!\x1a\x07\x01\x00".
		match: func(b []byte) bool { return bytes.HasPrefix(b, []byte("Rar!\x1a\x07")) },
	},
	{
		format:     "7z",
		correctExt: ".cb7",
		match:      func(b []byte) bool { return bytes.HasPrefix(b, []byte("7z\xbc\xaf\x27\x1c")) },
	},
	{
		format:     "ACE",
		correctExt: ".cba",
		// ACE stores the "**ACE**" signature at offset 7.
		match: func(b []byte) bool { return len(b) >= 14 && bytes.Equal(b[7:14], []byte("**ACE**")) },
	},
	{
		format:     "tar",
		correctExt: ".cbt",
		// POSIX/ustar tar stores the "ustar" magic at offset 257.
		match: func(b []byte) bool { return len(b) >= 262 && bytes.Equal(b[257:262], []byte("ustar")) },
	},
}

// sniffArchive inspects the header of path and, if the file is a recognised
// non-zip archive, returns a non-nil *mismatchedArchive describing what it
// really is. ZIP files and unrecognised content return (nil, nil).
func sniffArchive(path string) (*mismatchedArchive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 262 bytes is enough for every signature we test (tar's is at 257).
	buf := make([]byte, 262)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]

	for _, sig := range archiveSignatures {
		if sig.match(buf) {
			return &mismatchedArchive{correctExt: sig.correctExt, format: sig.format}, nil
		}
	}
	return nil, nil
}
