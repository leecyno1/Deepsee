package model

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionPlainTextLimitCountsRunes(t *testing.T) {
	session := &Session{Content: "甲乙丙丁"}
	got := session.PlainText(3)
	if !utf8.ValidString(got) {
		t.Fatalf("PlainText returned invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "甲乙丙 <...>") {
		t.Fatalf("PlainText = %q", got)
	}
}

func TestSessionV4Keeps64BitTimestamp(t *testing.T) {
	const timestamp int64 = 4_000_000_000
	got := (&SessionV4{
		LastTimestamp:         timestamp,
		UnreadCount:           7,
		LastMsgType:           MessageTypeShare,
		LastMsgSubType:        MessageSubTypeFile,
		LastMsgSender:         "wxid_sender",
		LastSenderDisplayName: "Sender",
	}).Wrap()
	if got.NOrder != timestamp || got.NTime.Unix() != timestamp {
		t.Fatalf("session timestamp narrowed: order=%d time=%d", got.NOrder, got.NTime.Unix())
	}
	if got.UnreadCount != 7 || got.LastMsgType != MessageTypeShare ||
		got.LastMsgSubType != MessageSubTypeFile || got.LastMsgSender != "wxid_sender" {
		t.Fatalf("session metadata lost: %#v", got)
	}
}
