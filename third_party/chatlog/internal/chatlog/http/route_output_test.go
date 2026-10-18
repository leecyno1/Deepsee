package http

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/model"
)

func TestWriteJSONUsesSingleOutputContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	writeJSON(ctx, gin.H{"ok": true})

	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q, want JSON", got)
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != `{"ok":true}` {
		t.Fatalf("body = %q", got)
	}
}

func TestHistoryMessageKeepsParsedAndDatabaseIdentifiers(t *testing.T) {
	message := &model.Message{
		ID:        1_700_000_000_000_042,
		DBLocalID: 42,
		Time:      time.Unix(1_700_000_000, 0),
		Sender:    "wxid_sender",
		IsSelf:    true,
		Type:      model.MessageTypeShare,
		SubType:   model.MessageSubTypeQuote,
		Contents:  map[string]interface{}{"title": "引用内容"},
		AtUserList: []string{
			"wxid_mentioned",
		},
	}
	row := toHistoryMessage(message, "")
	out := toHistoryMessageOut(row)

	if out.LocalID != message.ID || out.MessageID != message.ID || out.DBLocalID != message.DBLocalID {
		t.Fatalf("identifier mapping = local:%d message:%d db:%d", out.LocalID, out.MessageID, out.DBLocalID)
	}
	if out.Type != "share" || out.TypeNum != model.MessageTypeShare || out.SubType != model.MessageSubTypeQuote {
		t.Fatalf("type mapping = %q/%d/%d", out.Type, out.TypeNum, out.SubType)
	}
	if out.SenderID != message.Sender || !out.IsSelf || out.Contents["title"] != "引用内容" ||
		len(out.AtUserList) != 1 || out.AtUserList[0] != "wxid_mentioned" {
		t.Fatalf("preview fields = %#v", out)
	}
}

func TestHistoryMessageMissingOptionalFieldsStayEmpty(t *testing.T) {
	out := toHistoryMessageOut(gin.H{
		"timestamp":  int64(1),
		"message_id": int64(2),
	})

	if out.Chat != "" || out.MediaType != "" || out.MediaKey != "" || out.ImageKey != "" {
		t.Fatalf("missing optional fields leaked placeholder values: %#v", out)
	}
}

func TestHistoryVoiceUsesServerIDWithoutAmbiguousLocalCandidates(t *testing.T) {
	message := &model.Message{
		ID:        1_782_860_438_000_021,
		DBLocalID: 21,
		Seq:       1_782_860_438_000_021,
		Time:      time.Unix(1_782_860_438, 0),
		Sender:    "wxid_sender",
		Type:      model.MessageTypeVoice,
		Contents: map[string]interface{}{
			"voice":          "8123089943839909378",
			"voice_local_id": "21",
		},
	}

	out := toHistoryMessageOut(toHistoryMessage(message, "127.0.0.1:5030"))
	if out.MediaKey != "8123089943839909378" || out.MediaPath != "/voice/8123089943839909378" {
		t.Fatalf("primary voice mapping = %#v", out)
	}
	if len(out.MediaKeys) != 1 || out.MediaKeys[0] != "8123089943839909378" {
		t.Fatalf("voice candidates contain ambiguous local IDs: %#v", out.MediaKeys)
	}
}

func TestPaginateNewestMessagesUsesStableNonOverlappingPages(t *testing.T) {
	messages := make([]*model.Message, 0, 6)
	for i := int64(1); i <= 6; i++ {
		messages = append(messages, &model.Message{DBLocalID: i})
	}

	first := paginateNewestMessages(messages, 2, 0)
	second := paginateNewestMessages(messages, 2, 2)
	third := paginateNewestMessages(messages, 2, 4)

	assertIDs := func(name string, got []*model.Message, want ...int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s length = %d, want %d", name, len(got), len(want))
		}
		for i := range want {
			if got[i].DBLocalID != want[i] {
				t.Fatalf("%s[%d] = %d, want %d", name, i, got[i].DBLocalID, want[i])
			}
		}
	}
	assertIDs("first", first, 5, 6)
	assertIDs("second", second, 3, 4)
	assertIDs("third", third, 1, 2)
}

func TestClassifyChatTypeSeparatesBrandHolderFromOfficialAccounts(t *testing.T) {
	tests := []struct {
		username string
		want     string
	}{
		{username: "BrandSessionHolder", want: "folded"},
		{username: "brandprivatemsg@hardcode", want: "folded"},
		{username: "gh_e7e71fe92c19", want: "official_account"},
		{username: "friend", want: "private"},
		{username: "room@chatroom", want: "group"},
	}
	for _, test := range tests {
		if got := classifyChatType(test.username); got != test.want {
			t.Errorf("classifyChatType(%q) = %q, want %q", test.username, got, test.want)
		}
	}
}
