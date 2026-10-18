package chatlog

import (
	"testing"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	iwechat "github.com/sjzar/chatlog/internal/wechat"
)

func TestHydrateAccountsFromHistoryRestoresSinglePlaceholderAccount(t *testing.T) {
	instances := []*iwechat.Account{{Name: "未登录微信_42", PID: 42, Platform: "darwin", Version: 4}}
	history := map[string]conf.ProcessConfig{
		"alice": {Account: "alice", DataDir: `C:\\WeChat\\alice`, DataKey: "key", ImgKey: "image"},
	}
	got := hydrateAccountsFromHistory(instances, history, "alice")
	if len(got) != 1 || got[0].Name != "alice" || got[0].DataDir == "" || got[0].Key != "key" {
		t.Fatalf("unexpected hydrated account: %#v", got)
	}
	if instances[0].Name != "未登录微信_42" {
		t.Fatal("input account was mutated")
	}
}
