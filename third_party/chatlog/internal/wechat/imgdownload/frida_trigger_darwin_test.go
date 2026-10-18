//go:build darwin

package imgdownload

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedFridaScriptMaterializesOnce(t *testing.T) {
	t.Setenv("CHATLOG_DIR", t.TempDir())
	path, err := materializeEmbeddedImageDownloadFridaScript()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, embeddedImageDownloadFridaScript) {
		t.Fatal("materialized script differs from embedded asset")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstMTime := info.ModTime()
	time.Sleep(10 * time.Millisecond)
	again, err := materializeEmbeddedImageDownloadFridaScript()
	if err != nil || again != path {
		t.Fatalf("second materialize path=%q err=%v", again, err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstMTime) {
		t.Fatalf("unchanged embedded resource was rewritten: %s -> %s", firstMTime, info.ModTime())
	}
}

func TestEmbeddedFridaScriptMatchesDevelopmentCopy(t *testing.T) {
	development, err := os.ReadFile("../../../scripts/wechat_image_download_frida.py")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(development, embeddedImageDownloadFridaScript) {
		t.Fatal("embedded image-download script is out of sync with scripts/ copy")
	}
}

func TestEmbeddedFridaScriptUsesPrearmedTalkerScope(t *testing.T) {
	for _, marker := range []string{
		"readRequestScopeKey",
		"scope_key: scopeKey",
		"set_scope_talkers",
		"key.startswith(talker + \"_\")",
	} {
		if !bytes.Contains(embeddedImageDownloadFridaScript, []byte(marker)) {
			t.Fatalf("embedded script missing talker-scope marker %q", marker)
		}
	}
}

func TestEmbeddedFridaScriptKeepsReplayBehindExplicitFlag(t *testing.T) {
	for _, marker := range []string{
		"req.writeU64(uint64(1))",
		"requestKey || mediaKey || fileId",
		"ObjC.schedule(ObjC.mainQueue, run)",
		"--request-key",
	} {
		if !bytes.Contains(embeddedImageDownloadFridaScript, []byte(marker)) {
			t.Fatalf("embedded script missing native replay marker %q", marker)
		}
	}
	if bytes.Contains(embeddedImageDownloadFridaScript, []byte("Memory.copy(req, captured.template")) {
		t.Fatal("native replay still copies stale request pointers")
	}
	if !bytes.Contains(embeddedImageDownloadFridaScript, []byte("if not args.active_invoke")) {
		t.Fatal("synthetic replay is not guarded by the explicit CLI flag")
	}
}

func TestActiveReplayRequiresExplicitEnvironmentFlag(t *testing.T) {
	t.Setenv("CHATLOG_FRIDA_ACTIVE_INVOKE", "")
	if ActiveReplayEnabled() {
		t.Fatal("synthetic replay enabled by default")
	}
	t.Setenv("CHATLOG_FRIDA_ACTIVE_INVOKE", "1")
	if !ActiveReplayEnabled() {
		t.Fatal("explicit synthetic replay flag was ignored")
	}
}

func TestParseFridaJSONAndHumanize(t *testing.T) {
	raw := []byte(`noise before
{"ok": false, "pid": 1, "error": "no_manager_captured: open any image detail in WeChat once to prime CDN manager, then retry", "invoke": {"ok": false, "error": "no_manager_captured"}}
`)
	res, err := parseFridaJSON(raw)
	if err != nil {
		t.Fatalf("parseFridaJSON: %v", err)
	}
	if res.OK {
		t.Fatal("expected ok=false")
	}
	if !strings.Contains(res.Error, "no_manager_captured") {
		t.Fatalf("error field: %q", res.Error)
	}
	msg := humanizeFridaError(res, nil, raw)
	if !strings.Contains(msg, "CDN") && !strings.Contains(msg, "打开") {
		t.Fatalf("humanize = %q", msg)
	}
}

func TestParseFridaJSONLastLineWins(t *testing.T) {
	raw := []byte("{\"ok\":true}\n{\"ok\":false,\"error\":\"wechat not running\"}\n")
	res, err := parseFridaJSON(raw)
	if err != nil {
		t.Fatalf("parseFridaJSON: %v", err)
	}
	if res.OK {
		t.Fatalf("expected last object ok=false, got %+v", res)
	}
	if !strings.Contains(strings.ToLower(res.Error), "wechat not running") {
		t.Fatalf("error = %q", res.Error)
	}
	msg := humanizeFridaError(res, nil, raw)
	if !strings.Contains(msg, "微信未运行") {
		t.Fatalf("humanize = %q", msg)
	}
}
