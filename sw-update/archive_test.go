package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The testdata archives were made with 7-Zip from hello.txt + sub/big.txt;
// encrypted ones use the password "secret".
var fixtureFiles = map[string]string{
	"hello.txt":   "hello archive\n",
	"sub/big.txt": strings.Repeat("compressible line\r\n", 500),
}

func assertExtracted(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(got) != content {
			t.Errorf("%s: got %d bytes, want %d", name, len(got), len(content))
		}
	}
}

func TestExtractArchives(t *testing.T) {
	tests := []struct {
		file     string
		password string
	}{
		{"plain.zip", ""},
		{"plain.zip", "unused"},
		{"zipcrypto.zip", "secret"},
		{"zipcrypto-store.zip", "secret"},
		{"plain.7z", ""},
		{"enc.7z", "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.file+"/"+tt.password, func(t *testing.T) {
			dest := t.TempDir()
			extract := extractZip
			if strings.HasSuffix(tt.file, ".7z") {
				extract = extract7z
			}
			if err := extract(filepath.Join("testdata", tt.file), dest, tt.password); err != nil {
				t.Fatal(err)
			}
			assertExtracted(t, dest, fixtureFiles)
		})
	}
}

func TestExtractArchivesBadPassword(t *testing.T) {
	tests := []struct {
		file     string
		password string
	}{
		{"zipcrypto.zip", ""},
		{"zipcrypto.zip", "wrong"},
		{"zipcrypto-store.zip", "wrong"},
		{"enc.7z", ""},
		{"enc.7z", "wrong"},
	}
	for _, tt := range tests {
		t.Run(tt.file+"/"+tt.password, func(t *testing.T) {
			extract := extractZip
			if strings.HasSuffix(tt.file, ".7z") {
				extract = extract7z
			}
			err := extract(filepath.Join("testdata", tt.file), t.TempDir(), tt.password)
			if !errors.Is(err, errArchivePassword) {
				t.Fatalf("got %v, want errArchivePassword", err)
			}
		})
	}
}

func TestExtractZipAESUnsupported(t *testing.T) {
	err := extractZip(filepath.Join("testdata", "aes.zip"), t.TempDir(), "secret")
	if err == nil || !strings.Contains(err.Error(), "AES") {
		t.Fatalf("got %v, want AES unsupported error", err)
	}
}

// zipCryptoEncrypt encrypts data with ZipCrypto, prefixing the 12-byte header
// whose last byte is check.
func zipCryptoEncrypt(data []byte, password string, check byte) []byte {
	z := &zipCryptoReader{keys: [3]uint32{0x12345678, 0x23456789, 0x34567890}}
	for i := 0; i < len(password); i++ {
		z.update(password[i])
	}
	plain := append(make([]byte, 11), check)
	plain = append(plain, data...)
	out := make([]byte, len(plain))
	for i, b := range plain {
		t := uint16(z.keys[2]) | 2
		out[i] = b ^ byte((uint32(t)*uint32(t^1))>>8)
		z.update(b)
	}
	return out
}

// With a data descriptor (flag 0x8) the header check byte comes from the DOS
// mod time, not the CRC; 7-Zip never writes that layout, so build one by hand.
func TestExtractZipCryptoDataDescriptor(t *testing.T) {
	content := []byte(fixtureFiles["sub/big.txt"])
	var comp bytes.Buffer
	fw, _ := flate.NewWriter(&comp, flate.DefaultCompression)
	_, _ = fw.Write(content)
	_ = fw.Close()

	const dosTime = 0x7a3c
	hdr := &zip.FileHeader{
		Name:               "big.txt",
		Method:             zip.Deflate,
		Flags:              0x1 | 0x8,
		CRC32:              crc32.ChecksumIEEE(content),
		UncompressedSize64: uint64(len(content)),
	}
	hdr.ModifiedTime = dosTime //lint:ignore SA1019 the check byte reads this raw DOS field
	enc := zipCryptoEncrypt(comp.Bytes(), "secret", byte(dosTime>>8))
	hdr.CompressedSize64 = uint64(len(enc))

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(enc)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dd.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := extractZip(path, dest, "secret"); err != nil {
		t.Fatal(err)
	}
	assertExtracted(t, dest, map[string]string{"big.txt": string(content)})
}

func TestWriteEntryRejectsZipSlip(t *testing.T) {
	err := writeEntry(t.TempDir(), "../evil.txt", false, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("x")), nil
	})
	if err == nil || !strings.Contains(err.Error(), "zip slip") {
		t.Fatalf("got %v, want zip slip error", err)
	}
}
