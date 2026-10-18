//go:build darwin

package imgdownload

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneFridaCacheKeepsActiveRecentAndRollback(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	makeCache := func(name string, age time.Duration, size int) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "frida-helper"), make([]byte, size), 0o700); err != nil {
			t.Fatal(err)
		}
		when := now.Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		return path
	}
	active := makeCache("frida-active", time.Hour, 11)
	rollback := makeCache("frida-rollback", 30*time.Minute, 13)
	old := makeCache("frida-old", 2*time.Hour, 17)
	recent := makeCache("frida-recent", time.Minute, 19)
	other := makeCache("other-cache", 3*time.Hour, 23)

	result, err := pruneFridaCache(root, active+"/frida-helper socket-fd:9", 1, 2*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedDirectories != 1 || result.RemovedBytes != 17 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old cache still exists: %v", err)
	}
	for _, path := range []string{active, rollback, recent, other} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved path %s: %v", path, err)
		}
	}
}
