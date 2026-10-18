package dat2img

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestScanAndSetXorKeyReadsOnlyHeaderAndTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large_t.dat")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	header := make([]byte, 15)
	copy(header, V4Format2.Header)
	binary.LittleEndian.PutUint32(header[10:14], 2)
	if _, err := file.Write(header); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	const sparseSize = int64(128 << 20)
	if err := file.Truncate(sparseSize); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	const wantKey = byte(0x5a)
	if _, err := file.Seek(-2, 2); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{JpgTail[0] ^ wantKey, JpgTail[1] ^ wantKey}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	previous := V4XorKey
	t.Cleanup(func() { V4XorKey = previous })
	got, err := ScanAndSetXorKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantKey {
		t.Fatalf("xor key = 0x%x, want 0x%x", got, wantKey)
	}
}

func TestReadImageKeyPrefixBoundsLargeFileRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.dat")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 15+16)
	copy(want, V4Format2.Header)
	if _, err := file.Write(want); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Truncate(128 << 20); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := readImageKeyPrefix(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("prefix length/content mismatch: got %d bytes", len(got))
	}
}
