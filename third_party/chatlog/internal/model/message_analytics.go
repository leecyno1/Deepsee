package model

import (
	"context"
	"time"
)

// MessageAnalyticsRequest describes one literal keyword analysis over the
// message search index. Talkers is empty for all conversations.
type MessageAnalyticsRequest struct {
	StartTime   time.Time
	EndTime     time.Time
	Talkers     []string
	Keyword     string
	MessageType int64
}

type MessageAnalyticsCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type MessageAnalyticsChatCount struct {
	UserName string `json:"username"`
	Count    int64  `json:"count"`
}

type MessageAnalyticsResult struct {
	Keyword          string                      `json:"keyword"`
	Total            int64                       `json:"total"`
	FirstMessageTime int64                       `json:"first_message_time"`
	LastMessageTime  int64                       `json:"last_message_time"`
	Path             string                      `json:"search_path"`
	ByType           []MessageAnalyticsCount     `json:"by_type"`
	ByChat           []MessageAnalyticsChatCount `json:"by_chat"`
	ByDay            []MessageAnalyticsCount     `json:"by_day"`
}

// MessageAnalyticsQuery is implemented by database adapters that can aggregate
// directly from their full-text candidate set.
type MessageAnalyticsQuery interface {
	AnalyzeMessages(context.Context, MessageAnalyticsRequest) (MessageAnalyticsResult, error)
}
