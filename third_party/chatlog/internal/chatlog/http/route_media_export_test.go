package http

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolvePlainImageAttachmentFromMessageFile(t *testing.T) {
	root := t.TempDir()
	month, talker := "2026-08", "talker-hash"
	exported := filepath.Join(root, "msg", "file", month, "copied.jpg")
	rwTemp := filepath.Join(root, "temp", "RWTemp", month, talker, "copied.jpg")
	plain := filepath.Join(root, "msg", "attach", talker, month, "Img", "source.dat")
	distractor := filepath.Join(root, "msg", "attach", talker, month, "Img", "older.jpg")
	encrypted := []byte("encrypted-image-bytes")
	writeMediaFixture(t, exported, encrypted, time.Now())
	writeMediaFixture(t, rwTemp, encrypted, time.Now())
	jpeg := append([]byte{0xff, 0xd8, 0xff}, make([]byte, len(encrypted)-3)...)
	writeMediaFixture(t, plain, jpeg, time.Now().Add(-time.Minute))
	writeMediaFixture(t, distractor, jpeg, time.Now().Add(-time.Hour))
	if got := resolvePlainImageAttachment(root, exported); got != plain {
		t.Fatalf("resolved path = %q, want %q", got, plain)
	}
}

func TestResolvePlainImageAttachmentLeavesValidImageAlone(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "msg", "file", "2026-08", "valid.jpg")
	writeMediaFixture(t, path, []byte{0xff, 0xd8, 0xff, 0x00}, time.Now())
	if got := resolvePlainImageAttachment(root, path); got != path {
		t.Fatalf("valid image path changed to %q", got)
	}
}

func writeMediaFixture(t *testing.T, path string, data []byte, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}
