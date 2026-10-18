package http

import (
	"testing"
	"time"

	"github.com/sjzar/chatlog/internal/model"
)

func TestChatStatsSnapshotMergesOnlyRowsAfterWatermark(t *testing.T) {
	base := time.Unix(100, 0)
	snapshot := &chatStatsSnapshot{
		UserName:          "room@chatroom",
		ChatType:          "group",
		WatermarkMessages: make(map[string]struct{}),
		ByType:            make(map[string]int64),
		BySender:          make(map[string]int64),
		ActiveSenders:     make(map[string]struct{}),
		ActiveDays:        make(map[string]struct{}),
	}
	first := &model.Message{
		Seq: 1, DBLocalID: 1, Time: base, Talker: "room@chatroom",
		Sender: "alice", IsChatRoom: true, Type: model.MessageTypeText,
	}
	secondAtWatermark := &model.Message{
		Seq: 2, DBLocalID: 2, Time: base, Talker: "room@chatroom",
		Sender: "bob", IsChatRoom: true, Type: model.MessageTypeImage,
	}
	newer := &model.Message{
		Seq: 3, DBLocalID: 3, Time: base.Add(time.Second), Talker: "room@chatroom",
		Sender: "alice", IsChatRoom: true, Type: model.MessageTypeText, IsSelf: true,
	}
	snapshot.add(first)
	if added := snapshot.mergeNew([]*model.Message{first, secondAtWatermark, newer}); added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}
	response := snapshot.response("incremental", 2, base.Add(2*time.Second))
	if response.Total != 3 || response.WatermarkTime != newer.Time.Unix() {
		t.Fatalf("response = %+v", response)
	}
	if response.ActiveSenders != 2 || response.SentCount != 1 || response.ReceivedCount != 2 {
		t.Fatalf("sender/direction totals = %+v", response)
	}
	if response.ByType[0].Type != "text" || response.ByType[0].Count != 2 {
		t.Fatalf("type totals = %+v", response.ByType)
	}
}
