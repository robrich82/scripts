package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes data to a temp file with the given name and returns its path.
func writeSniffFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// tarLike returns a 512-byte block with the "ustar" magic at offset 257.
func tarLike() []byte {
	b := make([]byte, 512)
	copy(b[257:], []byte("ustar\x0000"))
	return b
}

// aceLike returns a header with the "**ACE**" signature at offset 7.
func aceLike() []byte {
	b := make([]byte, 32)
	copy(b[7:], []byte("**ACE**"))
	return b
}

func TestSniffArchive(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantExt string // "" means not a recognised non-zip archive
	}{
		{"rar4", []byte("Rar!\x1a\x07\x00rest"), ".cbr"},
		{"rar5", []byte("Rar!\x1a\x07\x01\x00rest"), ".cbr"},
		{"7z", []byte("7z\xbc\xaf\x27\x1crest"), ".cb7"},
		{"ace", aceLike(), ".cba"},
		{"tar", tarLike(), ".cbt"},
		{"zip", []byte("PK\x03\x04rest of a zip"), ""},
		{"unknown", []byte("not any known archive header"), ""},
		{"empty", []byte{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeSniffFile(t, "test.cbz", tt.data)
			mis, err := sniffArchive(path)
			if err != nil {
				t.Fatalf("sniffArchive: %v", err)
			}
			switch {
			case tt.wantExt == "" && mis != nil:
				t.Fatalf("expected no match, got %+v", mis)
			case tt.wantExt != "" && mis == nil:
				t.Fatalf("expected ext %q, got no match", tt.wantExt)
			case tt.wantExt != "" && mis.correctExt != tt.wantExt:
				t.Fatalf("expected ext %q, got %q", tt.wantExt, mis.correctExt)
			}
		})
	}
}
