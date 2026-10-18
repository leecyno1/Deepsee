package conf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestServerMessageHookSnapshotIsAtomic(t *testing.T) {
	cfg := &ServerConfig{}
	a := MessageHook{Keywords: "a", NotifyMode: "post", PostURL: "https://a.example", BeforeCount: 1, AfterCount: 1}
	b := MessageHook{Keywords: "b", NotifyMode: "qq", PostURL: "https://b.example", BeforeCount: 2, AfterCount: 2}
	if err := cfg.UpdateMessageHook(a); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			if i%2 == 0 {
				_ = cfg.UpdateMessageHook(a)
			} else {
				_ = cfg.UpdateMessageHook(b)
			}
		}
		close(done)
	}()
	go func() {
		defer wg.Done()
		for {
			snapshot := cfg.GetMessageHook()
			validA := snapshot.Keywords == "a" && snapshot.NotifyMode == "post" && snapshot.BeforeCount == 1
			validB := snapshot.Keywords == "b" && snapshot.NotifyMode == "qq" && snapshot.BeforeCount == 2
			if !validA && !validB {
				t.Errorf("observed mixed hook snapshot: %#v", snapshot)
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	wg.Wait()
}

func TestMessageHookKeywordModeNormalization(t *testing.T) {
	if got := NormalizeMessageHook(MessageHook{}).KeywordMode; got != HookKeywordModeText {
		t.Fatalf("default keyword mode = %q", got)
	}
	if got := NormalizeMessageHook(MessageHook{KeywordMode: " IMAGE_OCR "}).KeywordMode; got != HookKeywordModeImageOCR {
		t.Fatalf("image OCR keyword mode = %q", got)
	}
	if got := NormalizeMessageHook(MessageHook{KeywordMode: " MIXED "}).KeywordMode; got != HookKeywordModeMixed {
		t.Fatalf("mixed keyword mode = %q", got)
	}
	if !HookKeywordModeIncludesText(HookKeywordModeMixed) || !HookKeywordModeIncludesImageOCR(HookKeywordModeMixed) {
		t.Fatal("mixed keyword mode did not include both text and image OCR")
	}
	if IsHookKeywordMode("video") {
		t.Fatal("unsupported keyword mode passed validation")
	}
}

func TestMessageHookEffectiveTimeChangesOnlyWithRules(t *testing.T) {
	cfg := &ServerConfig{}
	first := MessageHook{Keywords: "a", EffectiveAt: 100, NotifyMode: HookNotifyPost}
	if err := cfg.UpdateMessageHook(first); err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateMessageHook(MessageHook{Keywords: "a", NotifyMode: HookNotifyQQ}); err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetMessageHook().EffectiveAt; got != 100 {
		t.Fatalf("delivery-only edit moved effective time to %d", got)
	}
	if err := cfg.UpdateMessageHook(MessageHook{Keywords: "b", EffectiveAt: 200, NotifyMode: HookNotifyQQ}); err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetMessageHook().EffectiveAt; got != 200 {
		t.Fatalf("rule edit effective time = %d", got)
	}
}

func TestScrubConfigSecretsHandlesStructAndMapKeyStyles(t *testing.T) {
	payload := map[string]any{
		"DataKey":     "data-secret",
		"img_key":     "image-secret",
		"HookPostURL": "https://example.com/hook?token=secret",
		"nested": map[string]any{
			"clientSecret": "client-secret",
			"safe":         "visible",
		},
	}
	scrubConfigSecrets(payload)

	for _, key := range []string{"DataKey", "img_key", "HookPostURL"} {
		if got := payload[key]; got != "******" {
			t.Fatalf("%s = %q, want redacted", key, got)
		}
	}
	nested := payload["nested"].(map[string]any)
	if got := nested["clientSecret"]; got != "******" {
		t.Fatalf("clientSecret = %q, want redacted", got)
	}
	if got := nested["safe"]; got != "visible" {
		t.Fatalf("safe = %q, want visible", got)
	}
}

func TestLoadTUIConfigMigratesLegacyDefaultWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".chatlog")
	account := "wxid_example"
	legacyRoot := filepath.Join(home, "Documents", "chatlog")
	legacyWorkDir := filepath.Join(legacyRoot, account)
	if err := os.MkdirAll(legacyWorkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyWorkDir, "message.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"last_account": account,
		"history": []map[string]any{{
			"account": account, "work_dir": legacyWorkDir,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "chatlog.json")
	if err := os.WriteFile(configFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := LoadTUIConfig(configDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(configDir, "workspaces", account)
	if len(cfg.History) != 1 || cfg.History[0].WorkDir != want {
		t.Fatalf("history work dir = %#v, want %q", cfg.History, want)
	}
	if _, err := os.Stat(filepath.Join(want, "message.db")); err != nil {
		t.Fatalf("workspace not moved: %v", err)
	}
	if _, err := os.Stat(legacyWorkDir); !os.IsNotExist(err) {
		t.Fatalf("legacy workspace still exists: %v", err)
	}

	persisted, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONPath(string(persisted), want) {
		t.Fatalf("migrated path not persisted: %s", persisted)
	}
}

func TestLoadServiceConfigDefaultsWorkDirUnderConfigRoot(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "chatlog")
	dataDir := filepath.Join(t.TempDir(), "wxid_example", "db_storage")
	cfg, _, err := LoadServiceConfig(configDir, map[string]any{
		"data_dir": dataDir,
		"data_key": "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(configDir, "workspaces", "wxid_example")
	if cfg.WorkDir != want {
		t.Fatalf("work dir = %q, want %q", cfg.WorkDir, want)
	}
	if cfg.GetRuntimeDir() != configDir {
		t.Fatalf("runtime dir = %q, want %q", cfg.GetRuntimeDir(), configDir)
	}
}

func containsJSONPath(raw, path string) bool {
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return false
	}
	history, ok := payload["history"].([]any)
	if !ok {
		return false
	}
	for _, item := range history {
		if fields, ok := item.(map[string]any); ok && fields["work_dir"] == path {
			return true
		}
	}
	return false
}
