package http

import (
	"testing"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

func TestEnrichHookEventProxyFieldsUsesMediaPath(t *testing.T) {
	events := []ports.HookEvent{{
		TriggerType: model.MessageTypeImage,
		TriggerContents: map[string]interface{}{
			"md5":  "index-not-ready",
			"path": "msg/attach/account/2026-08/Img/trigger",
		},
		Context: []ports.HookContextMessage{{
			Type: model.MessageTypeVideo,
			Contents: map[string]interface{}{
				"md5":  "video-index-not-ready",
				"path": "msg/attach/account/2026-08/Video/context",
			},
		}},
	}}

	enrichHookEventProxyFields(events)

	if got := events[0].TriggerContents["proxyKey"]; got != "msg/attach/account/2026-08/Img/trigger" {
		t.Fatalf("trigger proxy key = %#v", got)
	}
	if got := events[0].Context[0].Contents["proxyKey"]; got != "msg/attach/account/2026-08/Video/context" {
		t.Fatalf("context proxy key = %#v", got)
	}
}
