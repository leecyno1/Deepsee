package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWritePrivateFileAtomicReplacesContentAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secret.json")
	if err := WritePrivateFileAtomic(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFileAtomic(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "second" {
		t.Fatalf("content = %q, want second", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}
