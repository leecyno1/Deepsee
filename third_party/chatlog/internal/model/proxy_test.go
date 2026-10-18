package model

import "testing"

func TestRefreshProxyFields(t *testing.T) {
	message := &Message{
		Type: MessageTypeImage,
		Contents: map[string]interface{}{
			"md5": "abc123",
		},
	}
	message.RefreshProxyFields()
	if got := message.Contents["proxyType"]; got != "image" {
		t.Fatalf("proxyType = %#v", got)
	}
	if got := message.Contents["proxyUrl"]; got != "/image/abc123" {
		t.Fatalf("proxyUrl = %#v", got)
	}
	if got := message.Contents["resolved"]; got != true {
		t.Fatalf("resolved = %#v", got)
	}
}

func TestRefreshProxyFieldsPrefersConcreteImagePath(t *testing.T) {
	message := &Message{
		Type: MessageTypeImage,
		Contents: map[string]interface{}{
			"md5":  "index-not-ready",
			"path": "msg/attach/account/2026-08/Img/image-key",
		},
	}
	message.RefreshProxyFields()
	if got := message.Contents["proxyKey"]; got != "msg/attach/account/2026-08/Img/image-key" {
		t.Fatalf("image proxy key = %#v", got)
	}
	if got := message.Contents["keySource"]; got != "contents.path" {
		t.Fatalf("image key source = %#v", got)
	}
}

func TestRefreshProxyFieldsFallsBackToImageMD5(t *testing.T) {
	message := &Message{Type: MessageTypeImage, Contents: map[string]interface{}{"md5": "image-md5"}}
	message.RefreshProxyFields()
	if got := message.Contents["proxyKey"]; got != "image-md5" {
		t.Fatalf("image fallback proxy key = %#v", got)
	}
}

func TestRefreshProxyFieldsPrefersVoiceServerID(t *testing.T) {
	message := &Message{
		Type: MessageTypeVoice,
		Contents: map[string]interface{}{
			"voice":          "8123089943839909378",
			"voice_local_id": "21",
		},
	}
	message.RefreshProxyFields()
	if got := message.Contents["proxyKey"]; got != "8123089943839909378" {
		t.Fatalf("voice proxy key = %#v", got)
	}
	if got := message.Contents["keySource"]; got != "contents.voice" {
		t.Fatalf("voice key source = %#v", got)
	}
}

func TestRefreshProxyFieldsFallsBackWhenVoiceServerIDIsZero(t *testing.T) {
	message := &Message{
		Type: MessageTypeVoice,
		Contents: map[string]interface{}{
			"voice":          "0",
			"voice_local_id": "21",
		},
	}
	message.RefreshProxyFields()
	if got := message.Contents["proxyKey"]; got != "21" {
		t.Fatalf("voice fallback proxy key = %#v", got)
	}
	if got := message.Contents["keySource"]; got != "contents.voice_local_id" {
		t.Fatalf("voice fallback key source = %#v", got)
	}
}

func TestRecordAssetMetadata(t *testing.T) {
	item := DataItem{DataType: "8", DataTitle: "report.pdf", DataSize: "42"}
	item.SetResolvedProxy("file", "deadbeef", "hardlink_by_name_size")
	asset := item.ToAsset("1.2")
	if asset.ProxyURL != "/file/deadbeef" || asset.Size != 42 || !asset.Resolved {
		t.Fatalf("unexpected asset: %#v", asset)
	}
}
