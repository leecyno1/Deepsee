package common

import (
	"crypto/sha512"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDBFileRequiresCompleteSQLiteHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-plain.db")
	page := make([]byte, 4096)
	copy(page, []byte("SQLite format 3X"))
	if err := os.WriteFile(path, page, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDBFile(path, len(page)); err != nil {
		t.Fatalf("corrupt header was mistaken for plaintext: %v", err)
	}
}

func TestValidateKeyRejectsShortInputsWithoutPanic(t *testing.T) {
	derive := func(_, _ []byte) ([]byte, []byte) {
		return make([]byte, KeySize), make([]byte, KeySize)
	}
	if ValidateKey([]byte{1}, make([]byte, KeySize), []byte{1}, sha512.New, 64, 80, 4096, derive) {
		t.Fatal("short page unexpectedly validated")
	}
}

func TestDecryptPageRejectsShortPageWithoutPanic(t *testing.T) {
	if _, err := DecryptPage([]byte{1}, make([]byte, KeySize), nil, 1, nil, 0, 80, 4096); err == nil {
		t.Fatal("short page unexpectedly decrypted")
	}
}
