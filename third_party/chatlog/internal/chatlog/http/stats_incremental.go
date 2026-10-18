package http

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/model"
)

const (
	statsCacheFreshInterval  = 15 * time.Second
	statsFullRefreshInterval = 10 * time.Minute
)

type chatStatsSnapshot struct {
	Chat              string
	UserName          string
	ChatType          string
	QuerySince        int64
	QueryUntil        int64
	QueryRangeLabel   string
	Total             int
	SentCount         int
	ReceivedCount     int
	FirstMessageTime  int64
	LastMessageTime   int64
	WatermarkTime     int64
	WatermarkMessages map[string]struct{}
	ByType            map[string]int64
	BySender          map[string]int64
	ActiveSenders     map[string]struct{}
	ActiveDays        map[string]struct{}
	ByHour            [24]int
	FullRefreshAt     time.Time
}

func (s *Service) chatStats(
	chat string,
	rawTime string,
	start time.Time,
	end time.Time,
	force bool,
) (statsResponse, error) {
	s.statsComputeMu.Lock()
	defer s.statsComputeMu.Unlock()

	now := time.Now()
	key := statsCacheKey(chat, start, end)
	entry, cached := s.statsCache.Get(key)
	if cached && entry.Snapshot != nil && !force {
		if now.Before(entry.ExpiresAt) {
			return entry.Snapshot.response("hit", 0, now), nil
		}
		if !end.IsZero() && !end.After(now) {
			entry.ExpiresAt = now.Add(statsCacheFreshInterval)
			s.statsCache.Set(key, entry, statsCacheLimit)
			return entry.Snapshot.response("hit", 0, now), nil
		}
		if entry.Snapshot.WatermarkTime > 0 && now.Before(entry.Snapshot.FullRefreshAt) {
			incrementalStart := time.Unix(entry.Snapshot.WatermarkTime, 0)
			if !start.IsZero() && start.After(incrementalStart) {
				incrementalStart = start
			}
			messages, err := s.db.GetMessages(incrementalStart, end, chat, "", "", 0, 0)
			if err != nil {
				return statsResponse{}, err
			}
			added := entry.Snapshot.mergeNew(messages)
			entry.ExpiresAt = now.Add(statsCacheFreshInterval)
			entry.Payload = statsResponsePayload(entry.Snapshot.response("incremental", added, now))
			s.statsCache.Set(key, entry, statsCacheLimit)
			return entry.Snapshot.response("incremental", added, now), nil
		}
	}

	messages, err := s.db.GetMessages(start, end, chat, "", "", 0, 0)
	if err != nil {
		return statsResponse{}, err
	}
	snapshot := s.newChatStatsSnapshot(chat, rawTime, start, end, messages, now)
	response := snapshot.response("full", len(messages), now)
	s.statsCache.Set(key, statsCacheEntry{
		Payload:   statsResponsePayload(response),
		ExpiresAt: now.Add(statsCacheFreshInterval),
		Snapshot:  snapshot,
	}, statsCacheLimit)
	return response, nil
}

func statsCacheKey(chat string, start, end time.Time) string {
	var since, until int64
	if !start.IsZero() {
		since = start.Unix()
	}
	if !end.IsZero() {
		until = end.Unix()
	}
	return fmt.Sprintf("stats:v2:%s:%d:%d", strings.TrimSpace(chat), since, until)
}

func (s *Service) newChatStatsSnapshot(
	chat string,
	rawTime string,
	start time.Time,
	end time.Time,
	messages []*model.Message,
	now time.Time,
) *chatStatsSnapshot {
	snapshot := &chatStatsSnapshot{
		Chat:              chat,
		UserName:          chat,
		ChatType:          classifyChatType(chat),
		QueryRangeLabel:   statsRangeLabel(rawTime, start, end),
		WatermarkMessages: make(map[string]struct{}),
		ByType:            make(map[string]int64),
		BySender:          make(map[string]int64),
		ActiveSenders:     make(map[string]struct{}),
		ActiveDays:        make(map[string]struct{}),
		FullRefreshAt:     now.Add(statsFullRefreshInterval),
	}
	if !start.IsZero() {
		snapshot.QuerySince = start.Unix()
	}
	if !end.IsZero() {
		snapshot.QueryUntil = end.Unix()
	}
	if len(messages) > 0 {
		if talker := strings.TrimSpace(messages[0].Talker); talker != "" {
			snapshot.UserName = talker
			snapshot.ChatType = classifyChatType(talker)
		}
	}
	snapshot.Chat = s.resolveStatsChatName(snapshot.UserName, snapshot.ChatType, chat, messages)
	for _, message := range messages {
		snapshot.add(message)
	}
	return snapshot
}

func (s *Service) resolveStatsChatName(
	username string,
	chatType string,
	fallback string,
	messages []*model.Message,
) string {
	display := strings.TrimSpace(fallback)
	if display == "" {
		display = username
	}
	if chatType == "group" {
		if room, _ := s.db.GetChatRoom(username); room != nil {
			if name := strings.TrimSpace(room.DisplayName()); name != "" {
				return name
			}
		}
	} else if contact, _ := s.db.GetContact(username); contact != nil {
		if name := strings.TrimSpace(contact.DisplayName()); name != "" {
			return name
		}
	}
	if len(messages) > 0 {
		if name := strings.TrimSpace(messages[0].TalkerName); name != "" && display == fallback {
			return name
		}
	}
	return display
}

func statsRangeLabel(rawTime string, start, end time.Time) string {
	if strings.EqualFold(strings.TrimSpace(rawTime), "all") || (start.IsZero() && end.IsZero()) {
		return "全部时间"
	}
	switch {
	case !start.IsZero() && !end.IsZero():
		return fmt.Sprintf(
			"%s - %s",
			start.Format("2006-01-02 15:04"),
			end.Format("2006-01-02 15:04"),
		)
	case !start.IsZero():
		return fmt.Sprintf("自 %s 起", start.Format("2006-01-02 15:04"))
	case !end.IsZero():
		return fmt.Sprintf("截至 %s", end.Format("2006-01-02 15:04"))
	default:
		return "全部时间"
	}
}

func (snapshot *chatStatsSnapshot) mergeNew(messages []*model.Message) int {
	added := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		timestamp := message.Time.Unix()
		if timestamp < snapshot.WatermarkTime {
			continue
		}
		identity := statsMessageIdentity(message)
		if timestamp == snapshot.WatermarkTime {
			if _, exists := snapshot.WatermarkMessages[identity]; exists {
				continue
			}
		}
		snapshot.add(message)
		added++
	}
	return added
}

func (snapshot *chatStatsSnapshot) add(message *model.Message) {
	if message == nil {
		return
	}
	timestamp := message.Time.Unix()
	if timestamp > snapshot.WatermarkTime {
		snapshot.WatermarkTime = timestamp
		snapshot.WatermarkMessages = make(map[string]struct{})
	}
	if timestamp == snapshot.WatermarkTime {
		snapshot.WatermarkMessages[statsMessageIdentity(message)] = struct{}{}
	}
	snapshot.Total++
	snapshot.ByType[formatMessageType(message.Type)]++
	if message.IsSelf {
		snapshot.SentCount++
	} else {
		snapshot.ReceivedCount++
	}
	snapshot.ActiveDays[message.Time.Format("2006-01-02")] = struct{}{}
	if snapshot.FirstMessageTime == 0 || timestamp < snapshot.FirstMessageTime {
		snapshot.FirstMessageTime = timestamp
	}
	if timestamp > snapshot.LastMessageTime {
		snapshot.LastMessageTime = timestamp
	}
	if message.IsChatRoom {
		sender := strings.TrimSpace(message.SenderName)
		if sender == "" {
			sender = strings.TrimSpace(message.Sender)
		}
		if sender != "" {
			snapshot.BySender[sender]++
			snapshot.ActiveSenders[sender] = struct{}{}
		}
	}
	hour := message.Time.Hour()
	if hour >= 0 && hour < len(snapshot.ByHour) {
		snapshot.ByHour[hour]++
	}
}

func statsMessageIdentity(message *model.Message) string {
	if message == nil {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d", message.Talker, message.Seq, message.DBLocalID)
}

func (snapshot *chatStatsSnapshot) response(mode string, added int, now time.Time) statsResponse {
	typeRows := make([]statsCountByType, 0, len(snapshot.ByType))
	for messageType, count := range snapshot.ByType {
		typeRows = append(typeRows, statsCountByType{Type: messageType, Count: count})
	}
	sort.Slice(typeRows, func(i, j int) bool {
		if typeRows[i].Count == typeRows[j].Count {
			return typeRows[i].Type < typeRows[j].Type
		}
		return typeRows[i].Count > typeRows[j].Count
	})
	senderRows := make([]statsCountBySender, 0, len(snapshot.BySender))
	for sender, count := range snapshot.BySender {
		senderRows = append(senderRows, statsCountBySender{Sender: sender, Count: count})
	}
	sort.Slice(senderRows, func(i, j int) bool {
		if senderRows[i].Count == senderRows[j].Count {
			return senderRows[i].Sender < senderRows[j].Sender
		}
		return senderRows[i].Count > senderRows[j].Count
	})
	if len(senderRows) > 10 {
		senderRows = senderRows[:10]
	}
	hourRows := make([]statsCountByHour, 0, len(snapshot.ByHour))
	for hour, count := range snapshot.ByHour {
		hourRows = append(hourRows, statsCountByHour{Hour: hour, Count: count})
	}
	return statsResponse{
		Chat:             snapshot.Chat,
		UserName:         snapshot.UserName,
		IsGroup:          snapshot.ChatType == "group",
		ChatType:         snapshot.ChatType,
		Total:            snapshot.Total,
		SentCount:        snapshot.SentCount,
		ReceivedCount:    snapshot.ReceivedCount,
		ActiveSenders:    len(snapshot.ActiveSenders),
		ActiveDays:       len(snapshot.ActiveDays),
		FirstMessageTime: snapshot.FirstMessageTime,
		LastMessageTime:  snapshot.LastMessageTime,
		QuerySince:       snapshot.QuerySince,
		QueryUntil:       snapshot.QueryUntil,
		QueryRangeLabel:  snapshot.QueryRangeLabel,
		CacheMode:        mode,
		NewMessages:      added,
		WatermarkTime:    snapshot.WatermarkTime,
		GeneratedAt:      now.Unix(),
		ByType:           typeRows,
		TopSenders:       senderRows,
		ByHour:           hourRows,
	}
}

func statsResponsePayload(response statsResponse) gin.H {
	raw, err := json.Marshal(response)
	if err != nil {
		return gin.H{}
	}
	var payload gin.H
	if err := json.Unmarshal(raw, &payload); err != nil {
		return gin.H{}
	}
	return payload
}
