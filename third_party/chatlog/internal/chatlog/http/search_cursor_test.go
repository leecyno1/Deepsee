package http

import (
	"testing"

	"github.com/sjzar/chatlog/internal/model"
)

func TestMessageSearchCursorRoundTripAndQueryBinding(t *testing.T) {
	cursor := model.MessageSearchCursor{
		Score: 2, Timestamp: 100, Seq: 1001, LocalID: 7, Talker: "talker-a",
	}
	token := encodeMessageSearchCursor("query-a", cursor)
	decoded, err := decodeMessageSearchCursor(token, "query-a")
	if err != nil || decoded == nil || *decoded != cursor {
		t.Fatalf("decoded cursor = %+v, err=%v", decoded, err)
	}
	if _, err := decodeMessageSearchCursor(token, "query-b"); err == nil {
		t.Fatal("cursor accepted for a different search query")
	}
}

func TestSearchHighlightsUseRuneOffsets(t *testing.T) {
	terms, ranges := searchHighlights("前缀 Alpha 中文", []string{"alpha", "中文", "missing"})
	if len(terms) != 2 || len(ranges) != 2 {
		t.Fatalf("highlights = terms %+v ranges %+v", terms, ranges)
	}
	if ranges[0].Start != 3 || ranges[0].End != 8 {
		t.Fatalf("ASCII range after Chinese prefix = %+v", ranges[0])
	}
	if ranges[1].Start != 9 || ranges[1].End != 11 {
		t.Fatalf("Chinese range = %+v", ranges[1])
	}
}
