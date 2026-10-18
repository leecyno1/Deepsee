package messagehook

import (
	"reflect"
	"testing"

	"github.com/sjzar/chatlog/internal/model"
)

func TestExtractTriggerMediaRefPrefersConcreteImagePath(t *testing.T) {
	message := &model.Message{
		Type: model.MessageTypeImage,
		Contents: map[string]interface{}{
			"md5":  "index-not-ready",
			"path": "msg/attach/account/2026-08/Img/image-key",
		},
	}
	mediaType, keys := extractTriggerMediaRef(message)
	if mediaType != "image" {
		t.Fatalf("media type = %q", mediaType)
	}
	want := []string{"msg/attach/account/2026-08/Img/image-key", "index-not-ready"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("media keys = %#v, want %#v", keys, want)
	}
}
