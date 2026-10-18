package shared

import (
	"crypto/aes"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeAccountID(t *testing.T) {
	tests := map[string]string{
		" wxid_alpha_abcd ": "wxid_alpha",
		"account_abcd":      "account",
		"plain":             "plain",
	}
	for input, want := range tests {
		if got := NormalizeAccountID(input); got != want {
			t.Fatalf("NormalizeAccountID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFindTemplateData(t *testing.T) {
	directory := t.TempDir()
	content := make([]byte, 40)
	copy(content, []byte{0x07, 0x08, 0x56, 0x32, 0x08, 0x07})
	copy(content[0x0F:], []byte("0123456789abcdef"))
	content[len(content)-2] = 0x00
	content[len(content)-1] = 0x26
	if err := os.WriteFile(filepath.Join(directory, "sample_t.dat"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	template, ok := FindTemplateData(directory, 32)
	if !ok || string(template.Ciphertext) != "0123456789abcdef" {
		t.Fatalf("unexpected template: %#v, ok=%v", template, ok)
	}
	if template.XorKey == nil || *template.XorKey != 0xFF {
		t.Fatalf("unexpected XOR key: %#v", template.XorKey)
	}
}

func TestReadImageTemplateProbeSkipsFullLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large_t.dat")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 15+aes.BlockSize)
	copy(header, []byte{0x07, 0x08, 0x56, 0x32, 0x08, 0x07})
	if _, err := file.Write(header); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Truncate(maxImageKeyTemplateSize + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.Seek(-2, 2); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{0x01, 0x02}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	prefix, tail, full, err := readImageTemplateProbe(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefix) != 15+aes.BlockSize || string(tail) != string([]byte{0x01, 0x02}) {
		t.Fatalf("unexpected probe lengths/content: prefix=%d tail=%x", len(prefix), tail)
	}
	if full != nil {
		t.Fatalf("large template retained %d bytes", len(full))
	}
}

func TestVerifyImageKeyHeader(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := make([]byte, aes.BlockSize)
	copy(plain, []byte{0x89, 0x50, 0x4E, 0x47})
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, aes.BlockSize)
	block.Encrypt(ciphertext, plain)
	if !VerifyImageKeyHeader(key, ciphertext) {
		t.Fatal("expected PNG header to validate")
	}
}
