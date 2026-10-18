package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWrittenConfigUsesPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	m, err := New("chatlog-test", dir, "chatlog-test", "", true)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Value string `mapstructure:"value"`
	}
	if err := m.Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.SetConfig("value", "secret"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path string
		perm os.FileMode
	}{
		{path: dir, perm: 0700},
		{path: filepath.Join(dir, "chatlog-test.json"), perm: 0600},
	} {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != item.perm {
			t.Fatalf("%s permissions = %o, want %o", item.path, got, item.perm)
		}
	}
}
