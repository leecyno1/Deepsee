package chatlog

import (
	"testing"

	chatctx "github.com/sjzar/chatlog/internal/chatlog/ctx"
)

func TestSnapshotRedactsKeys(t *testing.T) {
	manager := &Manager{ctx: &chatctx.Context{
		Account: "alice",
		DataKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ImgKey:  "0123456789abcdef",
	}}

	snapshot := manager.Snapshot()
	if !snapshot.DataKeyPresent || !snapshot.ImageKeyPresent {
		t.Fatalf("expected present flags, got data=%v image=%v", snapshot.DataKeyPresent, snapshot.ImageKeyPresent)
	}
	if snapshot.DataKey != "******" || snapshot.ImageKey != "******" {
		t.Fatalf("snapshot leaked keys: data=%q image=%q", snapshot.DataKey, snapshot.ImageKey)
	}
}

func TestSnapshotLeavesMissingKeysEmpty(t *testing.T) {
	manager := &Manager{ctx: &chatctx.Context{Account: "alice"}}

	snapshot := manager.Snapshot()
	if snapshot.DataKeyPresent || snapshot.ImageKeyPresent {
		t.Fatalf("unexpected present flags: data=%v image=%v", snapshot.DataKeyPresent, snapshot.ImageKeyPresent)
	}
	if snapshot.DataKey != "" || snapshot.ImageKey != "" {
		t.Fatalf("missing keys should remain empty: data=%q image=%q", snapshot.DataKey, snapshot.ImageKey)
	}
}
