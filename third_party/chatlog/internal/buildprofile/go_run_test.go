package buildprofile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsGoRunTemporaryExecutable(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/var/folders/x/T/go-build123/b001/exe/chatlog_alpha", true},
		{"/tmp/go-build456/b001/exe/main", true},
		{"/Users/mac/Library/Caches/go-build/86/hash-d/chatlog", true},
		{"/Users/mac/Documents/chatlog/bin/chatlog", false},
		{"/Users/mac/Documents/go-build-project/bin/chatlog", false},
		{"/Applications/Chatlog.app/Contents/MacOS/chatlog", false},
	}
	for _, tc := range tests {
		if got := isGoRunTemporaryExecutable(tc.path); got != tc.want {
			t.Errorf("isGoRunTemporaryExecutable(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestFindModuleRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "internal", "buildprofile")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+modulePath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "build_chatlog_binary.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := findModuleRoot(nested); got != root {
		t.Fatalf("findModuleRoot() = %q, want %q", got, root)
	}
}
