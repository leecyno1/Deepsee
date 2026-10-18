package ctx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/wechat"
	clog "github.com/sjzar/chatlog/pkg/log"
)

func TestUpdateConfigWritesOnlyConfigRoot(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".chatlog")
	dataDir := filepath.Join(t.TempDir(), "wechat-data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := New(configDir)
	if err != nil {
		t.Fatal(err)
	}
	c.Account = "wxid_example"
	c.DataDir = dataDir
	c.WorkDir = filepath.Join(configDir, "workspaces", c.Account)
	c.UpdateConfig()

	if _, err := os.Stat(filepath.Join(configDir, "chatlog.json")); err != nil {
		t.Fatalf("root config missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "chatlog.json")); !os.IsNotExist(err) {
		t.Fatalf("project config leaked into WeChat data dir: %v", err)
	}
}

func TestSwitchCurrentSameAccountPreservesHTTPEnabled(t *testing.T) {
	c := &Context{
		Account:     "wxid_example",
		HTTPEnabled: false,
		HTTPAddr:    "127.0.0.1:5030",
		History: map[string]conf.ProcessConfig{
			"wxid_example": {
				Account:     "wxid_example",
				HTTPEnabled: true,
				HTTPAddr:    "127.0.0.1:9999",
			},
		},
	}
	// Stale History still says HTTP is enabled; runtime knows it is not.
	info := &wechat.Account{Name: "wxid_example", PID: 4321, Platform: "darwin", Version: 4, FullVersion: "4.1.12.25", Status: "online"}
	c.SwitchCurrent(info)
	if c.HTTPEnabled {
		t.Fatal("same-account PID refresh must not resurrect HTTPEnabled from stale History")
	}
	if c.HTTPAddr != "127.0.0.1:5030" {
		t.Fatalf("HTTPAddr clobbered: %q", c.HTTPAddr)
	}
	if c.PID != 4321 {
		t.Fatalf("PID not refreshed: %d", c.PID)
	}
}

func TestUpdateConfigSyncsInMemoryHistory(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".chatlog")
	c, err := New(configDir)
	if err != nil {
		t.Fatal(err)
	}
	c.Account = "wxid_example"
	c.HTTPEnabled = true
	c.HTTPAddr = "127.0.0.1:5030"
	c.UpdateConfig()
	c.HTTPEnabled = false
	c.UpdateConfig()
	if got := c.History["wxid_example"].HTTPEnabled; got {
		t.Fatal("in-memory History must track SetHTTPEnabled(false)")
	}
}

func TestLogRetentionDefaultsAndPersists(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".chatlog")
	c, err := New(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if c.LogRetentionDays != clog.DefaultRetentionDays {
		t.Fatalf("default retention = %d, want %d", c.LogRetentionDays, clog.DefaultRetentionDays)
	}
	c.Account = "wxid_example"
	c.SetLogRetentionDays(30)
	if got := c.History[c.Account].LogRetentionDays; got != 30 {
		t.Fatalf("persisted retention = %d, want 30", got)
	}
	reloaded, err := New(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LogRetentionDays != 30 {
		t.Fatalf("reloaded retention = %d, want 30", reloaded.LogRetentionDays)
	}
}

func TestUpdateOCRConfigPersistsRuntimeControlsAndScope(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".chatlog")
	c, err := New(configDir)
	if err != nil {
		t.Fatal(err)
	}
	c.Account = "wxid_example"
	next := conf.DefaultOCRConfig()
	next.Enabled = true
	next.BackfillEnabled = true
	auto := false
	next.LocalAutoStart = &auto
	next.LocalAutoRestart = &auto
	next.ListenContacts = "wxid_a,wxid_b"
	next.ListenChatRooms = "100@chatroom"
	if err := c.UpdateOCRConfig(next); err != nil {
		t.Fatal(err)
	}
	stored := c.GetStoredOCRConfig()
	if !stored.Enabled || !stored.BackfillEnabled ||
		stored.LocalAutoStartEnabled() || stored.LocalAutoRestartEnabled() ||
		!stored.AllowsTalker("wxid_a") || !stored.AllowsTalker("100@chatroom") {
		t.Fatalf("stored OCR config = %#v", stored)
	}
	history := c.History["wxid_example"].OCR
	if !history.Enabled || !history.BackfillEnabled || history.LocalAutoStartEnabled() ||
		history.LocalAutoRestartEnabled() || history.ListenContacts != "wxid_a,wxid_b" {
		t.Fatalf("history OCR config = %#v", history)
	}
}
