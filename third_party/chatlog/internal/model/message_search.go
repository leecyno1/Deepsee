package model

import (
	"strconv"
	"time"
)

// MessageSearchRequest describes the storage-independent message search
// contract. Match accepts phrase, all or any; Sort accepts time or relevance.
type MessageSearchRequest struct {
	StartTime   time.Time
	EndTime     time.Time
	Talkers     []string
	Keyword     string
	MessageType int64
	Limit       int
	Offset      int
	Match       string
	Sort        string
	Cursor      *MessageSearchCursor
}

// MessageSearchCursor is a stable position in the deterministic merged result
// order. Score is used only when relevance sorting is selected.
type MessageSearchCursor struct {
	Score     int
	Timestamp int64
	Seq       int64
	LocalID   int64
	Talker    string
}

type MessageSearchResult struct {
	Messages []*Message
	Total    int
	Path     string
	Terms    []string
	Match    string
	Sort     string
	Scores   map[string]int
}

func MessageSearchResultKey(message *Message) string {
	if message == nil {
		return ""
	}
	return message.Talker + ":" + strconv.FormatInt(message.Time.Unix(), 10) + ":" +
		strconv.FormatInt(message.Seq, 10) + ":" + strconv.FormatInt(message.DBLocalID, 10)
}
