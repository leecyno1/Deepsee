package imgdownload

import "testing"

func TestNativeRequestKeyUsesMessageIdentity(t *testing.T) {
	req := Request{
		Talker:      "57992622478@chatroom",
		MessageTime: 1785725152,
		DBLocalID:   1208,
		MediaKey:    "media-key",
	}
	if got, want := nativeRequestKey(req), "57992622478@chatroom_1785725152_1208_2"; got != want {
		t.Fatalf("nativeRequestKey() = %q, want %q", got, want)
	}
}

func TestNativeRequestKeyFallsBackToMediaKey(t *testing.T) {
	if got := nativeRequestKey(Request{MediaKey: " media-key "}); got != "media-key" {
		t.Fatalf("nativeRequestKey() = %q", got)
	}
}
