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
	"runtime"
	"strings"
	"testing"
)

// The testdata archives were made with 7-Zip from hello.txt + sub/big.txt;
// encrypted ones use the password "secret". enc-store.7z is stored (-mx0), so
// a wrong key never trips a decoder and only the CRC check catches it.
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

// copyFixture copies a testdata archive to a temp file named name, so tests
// can exercise extension handling.
func copyFixture(t *testing.T, fixture, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractArchive(t *testing.T) {
	tests := []struct {
		file     string
		password string
		wantErr  error // nil: extraction succeeds with the fixture contents
	}{
		{"plain.zip", "", nil},
		{"plain.zip", "unused", nil},
		{"zipcrypto.zip", "secret", nil},
		{"zipcrypto-store.zip", "secret", nil},
		{"plain.7z", "", nil},
		{"enc.7z", "secret", nil},
		{"enc-store.7z", "secret", nil},
		{"zipcrypto.zip", "", errArchivePassword},
		{"zipcrypto.zip", "wrong", errArchivePassword},
		{"zipcrypto-store.zip", "wrong", errArchivePassword},
		{"enc.7z", "", errArchivePassword},
		{"enc.7z", "wrong", errArchivePassword},
		{"enc-store.7z", "wrong", errArchivePassword},
		{"enc-store.7z", "", errChecksum},
	}
	for _, tt := range tests {
		t.Run(tt.file+"/"+tt.password, func(t *testing.T) {
			dest := t.TempDir()
			err := extractArchive(filepath.Join("testdata", tt.file), dest, tt.password)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("got %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertExtracted(t, dest, fixtureFiles)
		})
	}
}

func TestExtractArchiveUppercaseExtension(t *testing.T) {
	for _, tt := range []struct{ fixture, name string }{
		{"plain.zip", "APP.ZIP"},
		{"plain.7z", "APP.7Z"},
	} {
		dest := t.TempDir()
		if err := extractArchive(copyFixture(t, tt.fixture, tt.name), dest, ""); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		assertExtracted(t, dest, fixtureFiles)
	}
}

func TestPasswordErrHint(t *testing.T) {
	err := extractArchive(filepath.Join("testdata", "zipcrypto.zip"), t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "set archive_password") {
		t.Errorf("no password: got %v, want a set-archive_password hint", err)
	}
	err = extractArchive(filepath.Join("testdata", "zipcrypto.zip"), t.TempDir(), "wrong")
	if err == nil || !strings.Contains(err.Error(), "archive_password is wrong") {
		t.Errorf("wrong password: got %v, want an archive_password-is-wrong hint", err)
	}
}

func TestExtractZipAESUnsupported(t *testing.T) {
	err := extractArchive(filepath.Join("testdata", "aes.zip"), t.TempDir(), "secret")
	if err == nil || !strings.Contains(err.Error(), "AES") {
		t.Fatalf("got %v, want AES unsupported error", err)
	}
}

// zipCryptoEncrypt encrypts data with ZipCrypto, prefixing the 12-byte header
// whose last byte is check.
func zipCryptoEncrypt(data []byte, password string, check byte) []byte {
	z := newZipCryptoKeys(password)
	plain := append(make([]byte, 11), check)
	plain = append(plain, data...)
	out := make([]byte, len(plain))
	for i, b := range plain {
		out[i] = b ^ z.keystream()
		z.update(b)
	}
	return out
}

// writeZipCrypto writes a one-entry ZipCrypto zip encrypted with password and
// header check byte check.
func writeZipCrypto(t *testing.T, content []byte, method uint16, flags uint16, password string, check byte) string {
	t.Helper()
	payload := content
	if method == zip.Deflate {
		var comp bytes.Buffer
		fw, _ := flate.NewWriter(&comp, flate.DefaultCompression)
		_, _ = fw.Write(content)
		_ = fw.Close()
		payload = comp.Bytes()
	}
	hdr := &zip.FileHeader{
		Name:               "f.txt",
		Method:             method,
		Flags:              zipFlagEncrypted | flags,
		CRC32:              crc32.ChecksumIEEE(content),
		UncompressedSize64: uint64(len(content)),
	}
	hdr.ModifiedTime = 0x7a3c //nolint:staticcheck // SA1019: the check byte reads this raw DOS field
	enc := zipCryptoEncrypt(payload, password, check)
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
	path := filepath.Join(t.TempDir(), "f.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// With a data descriptor the header check byte is the DOS time's high byte per
// Info-ZIP, though some writers still use the CRC's; both must be accepted.
func TestExtractZipCryptoDataDescriptor(t *testing.T) {
	content := []byte(fixtureFiles["sub/big.txt"])
	for name, check := range map[string]byte{
		"time": byte(0x7a3c >> 8),
		"crc":  byte(crc32.ChecksumIEEE(content) >> 24),
	} {
		path := writeZipCrypto(t, content, zip.Deflate, zipFlagDataDescriptor, "secret", check)
		dest := t.TempDir()
		if err := extractArchive(path, dest, "secret"); err != nil {
			t.Fatalf("%s check byte: %v", name, err)
		}
		assertExtracted(t, dest, map[string]string{"f.txt": string(content)})
	}
}

// A wrong key passes the one-byte header check 1 time in 256; the CRC check
// must still reject it, and the garbage file must not be left behind.
func TestExtractZipCryptoWrongKeyPassingHeaderCheck(t *testing.T) {
	content := []byte(fixtureFiles["sub/big.txt"])
	// Encrypted under "other" with a check byte that "secret" decrypts to
	// match: forge it by decrypting the first 12 bytes under "secret".
	enc := zipCryptoEncrypt(content, "other", 0)
	z := newZipCryptoKeys("secret")
	var check byte
	for i := 0; i < 12; i++ {
		check = enc[i] ^ z.keystream()
		z.update(check)
	}
	path := writeZipCrypto(t, content, zip.Store, 0, "other", 0)
	// Rewrite the 12th header byte so "secret" decrypts it to the CRC byte.
	data, _ := os.ReadFile(path)
	idx := bytes.Index(data, enc[:11])
	if idx < 0 {
		t.Fatal("encrypted header not found")
	}
	want := byte(crc32.ChecksumIEEE(content) >> 24)
	data[idx+11] = enc[11] ^ check ^ want
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	err := extractArchive(path, dest, "secret")
	if !errors.Is(err, errArchivePassword) || !errors.Is(err, errChecksum) {
		t.Fatalf("got %v, want a password error caused by a checksum mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "f.txt")); !os.IsNotExist(statErr) {
		t.Errorf("garbage file left behind after failed extraction (stat: %v)", statErr)
	}
}

func TestWriteEntryRejectsUnsafeNames(t *testing.T) {
	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }
	names := []string{"../evil.txt", "a/../../evil.txt", "", ".", "/abs.txt"}
	if runtime.GOOS == "windows" { // elsewhere these are ordinary file names
		names = append(names, `C:\abs.txt`, "NUL")
	}
	for _, name := range names {
		dest := t.TempDir()
		if err := writeEntry(dest, name, false, open); err == nil {
			t.Errorf("%q: accepted, want rejected", name)
		}
	}
}
