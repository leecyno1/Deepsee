package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultGeneratedDirectoriesShareOneRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "chatlog-root")
	t.Setenv("CHATLOG_DIR", root)

	for _, path := range []string{
		DefaultWorkDir(""),
		DefaultWorkDir("wxid_example"),
		DefaultCacheDir("wcdb"),
		DefaultCacheDir("sns_wasm"),
		DefaultCacheDir("filecopy", "chatlog"),
	} {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || filepath.IsAbs(rel) {
			t.Fatalf("generated path escaped root: root=%q path=%q rel=%q err=%v", root, path, rel, err)
		}
	}
}

func TestWorkDirAtSanitizesAccount(t *testing.T) {
	root := t.TempDir()
	got := WorkDirAt(root, "../../wxid/example")
	wantRoot := filepath.Join(root, "workspaces")
	rel, err := filepath.Rel(wantRoot, got)
	if err != nil || rel == ".." || filepath.IsAbs(rel) {
		t.Fatalf("unsafe work dir %q: rel=%q err=%v", got, rel, err)
	}
	if filepath.Dir(got) != wantRoot {
		t.Fatalf("account created nested directories: %q", got)
	}
}

func TestMigrateLegacyDefaultWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".chatlog")
	account := "wxid_example"
	legacy := LegacyDefaultWorkDir(account)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "message.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, migrated, err := MigrateLegacyDefaultWorkDir(root, account, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("expected legacy default to migrate")
	}
	if want := WorkDirAt(root, account); got != want {
		t.Fatalf("migrated path = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(got, "message.db")); err != nil {
		t.Fatalf("migrated file missing: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy directory still exists: %v", err)
	}
}

func TestMigrateLegacyDefaultWorkDirPreservesCustomPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	custom := filepath.Join(home, "custom", "workspace")
	got, migrated, err := MigrateLegacyDefaultWorkDir(filepath.Join(home, ".chatlog"), "wxid_example", custom)
	if err != nil {
		t.Fatal(err)
	}
	if migrated || got != custom {
		t.Fatalf("custom path changed: got=%q migrated=%v", got, migrated)
	}
}

func TestMigrateLegacyDefaultWorkDirRejectsUnsafeAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	current := filepath.Join(home, "escape")
	got, migrated, err := MigrateLegacyDefaultWorkDir(filepath.Join(home, ".chatlog"), "../../escape", current)
	if err != nil {
		t.Fatal(err)
	}
	if migrated || got != current {
		t.Fatalf("unsafe account migrated: got=%q migrated=%v", got, migrated)
	}
}
