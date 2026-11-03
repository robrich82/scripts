package main

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
)

// errArchivePassword is returned when an archive is encrypted and no password,
// or the wrong one, was configured.
var errArchivePassword = errors.New("archive is password-protected: set archive_password for this app in sw-update.json")

// zipMethodAES is the compression method WinZip AES-encrypted entries use.
const zipMethodAES = 99

// extractZip extracts a zip archive to destDir, decrypting ZipCrypto entries
// with password.
func extractZip(zipPath, destDir, password string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	fmt.Printf("  Extracting %s...\n", filepath.Base(zipPath))

	for _, f := range r.File {
		err := writeEntry(destDir, f.Name, f.FileInfo().IsDir(), func() (io.ReadCloser, error) {
			return openZipEntry(f, password)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// extract7z extracts a 7z archive to destDir. password may be empty for
// unencrypted archives.
func extract7z(archivePath, destDir, password string) error {
	r, err := sevenzip.OpenReaderWithPassword(archivePath, password)
	if err != nil {
		return fmt.Errorf("open 7z %s: %w", archivePath, sevenZipPasswordErr(err))
	}
	defer r.Close()

	fmt.Printf("  Extracting %s...\n", filepath.Base(archivePath))

	for _, f := range r.File {
		err := writeEntry(destDir, f.Name, f.FileInfo().IsDir(), func() (io.ReadCloser, error) {
			return f.Open()
		})
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, sevenZipPasswordErr(err))
		}
	}
	return nil
}

// sevenZipPasswordErr reports read failures in an encrypted 7z as a password
// problem: a missing or wrong key only surfaces as a checksum or decode error.
func sevenZipPasswordErr(err error) error {
	var rpe *sevenzip.ReadError
	if errors.As(err, &rpe) && rpe.Encrypted {
		return fmt.Errorf("%w (%v)", errArchivePassword, err)
	}
	return err
}

// writeEntry writes one archive entry under destDir, rejecting paths that
// escape it ("zip slip").
func writeEntry(destDir, name string, isDir bool, open func() (io.ReadCloser, error)) error {
	destPath := filepath.Join(destDir, name)
	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(destPath)+string(os.PathSeparator), cleanDest) {
		return fmt.Errorf("zip slip detected: %s", name)
	}

	if isDir {
		return os.MkdirAll(destPath, 0755)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, rc)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// openZipEntry opens a zip entry, decrypting it if it is ZipCrypto-encrypted.
func openZipEntry(f *zip.File, password string) (io.ReadCloser, error) {
	if f.Flags&0x1 == 0 {
		return f.Open()
	}
	if f.Method == zipMethodAES {
		return nil, fmt.Errorf("%s: AES-encrypted zip entries are not supported", f.Name)
	}
	if password == "" {
		return nil, fmt.Errorf("%s: %w", f.Name, errArchivePassword)
	}

	raw, err := f.OpenRaw()
	if err != nil {
		return nil, err
	}
	// The 12-byte encryption header's last byte must decrypt to the high byte
	// of the CRC, or of the DOS mod time when a data descriptor follows.
	check := byte(f.CRC32 >> 24)
	if f.Flags&0x8 != 0 {
		check = byte(f.ModifiedTime >> 8)
	}
	dec, err := newZipCryptoReader(raw, password, check)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name, err)
	}

	var body io.ReadCloser
	switch f.Method {
	case zip.Store:
		body = io.NopCloser(dec)
	case zip.Deflate:
		body = flate.NewReader(dec)
	default:
		return nil, fmt.Errorf("%s: unsupported compression method %d", f.Name, f.Method)
	}
	return &crcCheckReader{rc: body, want: f.CRC32, name: f.Name}, nil
}

// zipCryptoReader decrypts the traditional PKWARE ("ZipCrypto") stream cipher.
type zipCryptoReader struct {
	r    io.Reader
	keys [3]uint32
}

func newZipCryptoReader(r io.Reader, password string, check byte) (*zipCryptoReader, error) {
	z := &zipCryptoReader{r: r, keys: [3]uint32{0x12345678, 0x23456789, 0x34567890}}
	for i := 0; i < len(password); i++ {
		z.update(password[i])
	}
	var hdr [12]byte
	if _, err := io.ReadFull(z, hdr[:]); err != nil {
		return nil, fmt.Errorf("reading encryption header: %w", err)
	}
	if hdr[11] != check {
		return nil, errArchivePassword
	}
	return z, nil
}

func (z *zipCryptoReader) update(b byte) {
	z.keys[0] = crc32.IEEETable[byte(z.keys[0])^b] ^ (z.keys[0] >> 8)
	z.keys[1] = (z.keys[1]+(z.keys[0]&0xff))*134775813 + 1
	z.keys[2] = crc32.IEEETable[byte(z.keys[2])^byte(z.keys[1]>>24)] ^ (z.keys[2] >> 8)
}

func (z *zipCryptoReader) Read(p []byte) (int, error) {
	n, err := z.r.Read(p)
	for i := 0; i < n; i++ {
		t := uint16(z.keys[2]) | 2
		p[i] ^= byte((uint32(t) * uint32(t^1)) >> 8)
		z.update(p[i])
	}
	return n, err
}

// crcCheckReader verifies the decompressed data against the entry's CRC-32 at
// EOF; OpenRaw bypasses the check zip.File.Open would normally do. The header
// check byte only has a 1-in-256 false-pass rate, so this is what reliably
// catches a wrong password.
type crcCheckReader struct {
	rc   io.ReadCloser
	want uint32
	crc  uint32
	name string
}

func (c *crcCheckReader) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.crc = crc32.Update(c.crc, crc32.IEEETable, p[:n])
	switch {
	case err == io.EOF && c.crc != c.want:
		return n, fmt.Errorf("%s: checksum mismatch: %w", c.name, errArchivePassword)
	case err != nil && err != io.EOF:
		// Data decrypted with the wrong key usually fails to inflate first.
		return n, fmt.Errorf("%s: %v: %w", c.name, err, errArchivePassword)
	}
	return n, err
}

func (c *crcCheckReader) Close() error { return c.rc.Close() }
