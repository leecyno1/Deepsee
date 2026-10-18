//go:build darwin

package darwin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractKeyStopsAfterStructuredCleanupEvenWhenPipeIsInherited(t *testing.T) {
	if !FridaAvailable() {
		t.Skip("Frida is not installed in the test user's Python environment")
	}

	const key = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	scriptPath := filepath.Join(t.TempDir(), "fake_frida_capture.py")
	script := `import json
import os
import time

print(json.dumps({"type": "key", "key": "` + key + `"}), flush=True)
print(json.dumps({"type": "cleanup", "message": "detached"}), flush=True)

# Simulate a helper briefly inheriting stdout. It exits as soon as the parent
# injector is killed, but would otherwise keep the pipe open.
if os.fork() == 0:
    while os.getppid() != 1:
        time.sleep(0.02)
    os._exit(0)

time.sleep(30)
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake Frida script: %v", err)
	}
	t.Setenv("CHATLOG_FRIDA_SCRIPT", scriptPath)
	t.Setenv("CHATLOG_FRIDA_MODE", "attach")
	t.Setenv("WECHAT_EXE", "/bin/echo")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	got, err := ExtractKeyViaFrida(ctx, "", nil)
	if err != nil {
		t.Fatalf("ExtractKeyViaFrida: %v", err)
	}
	if got != key {
		t.Fatalf("key = %q, want %q", got, key)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cleanup took %s; inherited stdout likely blocked completion", elapsed)
	}
}

func TestEmbeddedKeyFridaScriptMaterializesOnce(t *testing.T) {
	t.Setenv("CHATLOG_DIR", t.TempDir())
	path, err := materializeEmbeddedKeyFridaScript()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != embeddedFridaScript {
		t.Fatal("materialized key Frida script differs from embedded resource")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstMTime := info.ModTime()
	time.Sleep(10 * time.Millisecond)
	again, err := materializeEmbeddedKeyFridaScript()
	if err != nil || again != path {
		t.Fatalf("second materialize path=%q err=%v", again, err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstMTime) {
		t.Fatalf("unchanged key Frida script was rewritten: %s -> %s", firstMTime, info.ModTime())
	}
}

func TestEmbeddedKeyFridaScriptMatchesDevelopmentCopy(t *testing.T) {
	development, err := os.ReadFile("../../../../scripts/wechat_key_frida.py")
	if err != nil {
		t.Fatal(err)
	}
	if string(development) != embeddedFridaScript {
		t.Fatal("embedded key Frida script is out of sync with scripts/ copy")
	}
}
