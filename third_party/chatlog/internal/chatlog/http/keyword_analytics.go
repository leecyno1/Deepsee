package http

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/pkg/util"
)

func (s *Service) handleKeywordAnalytics(c *gin.Context) {
	keyword := strings.TrimSpace(c.Query("keyword"))
	if keyword == "" {
		errors.Err(c, errors.InvalidArg("keyword"))
		return
	}
	messageType, err := strconv.ParseInt(defaultString(strings.TrimSpace(c.Query("msg_type")), "0"), 10, 64)
	if err != nil || messageType < 0 {
		errors.Err(c, errors.InvalidArg("msg_type"))
		return
	}
	chatLimit, err := parseDatabaseQueryInteger(c.Query("chat_limit"), 20, 1, 100)
	if err != nil {
		errors.Err(c, errors.InvalidArg("chat_limit"))
		return
	}
	start, end, _, err := parseSinceUntil(c.Query("time"), c.Query("since"), c.Query("until"))
	if err != nil {
		errors.Err(c, err)
		return
	}
	chats := util.Str2List(c.Query("chats"), ",")
	cacheKey := keywordAnalyticsCacheKey(keyword, chats, start, end, messageType, chatLimit)
	force := strings.TrimSpace(c.Query("force")) == "1" ||
		strings.EqualFold(strings.TrimSpace(c.Query("force")), "true")
	if !force {
		if payload, ok := s.getStatsCache(cacheKey); ok {
			payload["cache_mode"] = "hit"
			writeJSON(c, payload)
			return
		}
	}

	analyzer, ok := s.db.(MessageAnalyticsQuery)
	if !ok {
		errors.Err(c, fmt.Errorf("message analytics backend is not configured"))
		return
	}
	result, err := analyzer.AnalyzeMessages(c.Request.Context(), model.MessageAnalyticsRequest{
		StartTime:   start,
		EndTime:     end,
		Talkers:     chats,
		Keyword:     keyword,
		MessageType: messageType,
	})
	if err != nil {
		errors.Err(c, err)
		return
	}

	byType := make([]gin.H, 0, len(result.ByType))
	for _, item := range result.ByType {
		numeric, _ := strconv.ParseInt(item.Key, 10, 64)
		byType = append(byType, gin.H{
			"type":     formatMessageType(numeric),
			"type_num": numeric,
			"count":    item.Count,
		})
	}
	byChat := make([]gin.H, 0, min(chatLimit, len(result.ByChat)))
	for _, item := range result.ByChat {
		if len(byChat) >= chatLimit {
			break
		}
		chatType := classifyChatType(item.UserName)
		display := item.UserName
		if chatType == "group" {
			if room, _ := s.db.GetChatRoom(item.UserName); room != nil {
				if value := strings.TrimSpace(room.DisplayName()); value != "" {
					display = value
				}
			}
		} else if contact, _ := s.db.GetContact(item.UserName); contact != nil {
			if value := strings.TrimSpace(contact.DisplayName()); value != "" {
				display = value
			}
		}
		byChat = append(byChat, gin.H{
			"chat":      display,
			"username":  item.UserName,
			"chat_type": chatType,
			"count":     item.Count,
		})
	}
	byDay := make([]gin.H, 0, len(result.ByDay))
	for _, item := range result.ByDay {
		byDay = append(byDay, gin.H{"date": item.Key, "count": item.Count})
	}
	payload := gin.H{
		"keyword":            result.Keyword,
		"total":              result.Total,
		"chat_count":         len(result.ByChat),
		"day_count":          len(result.ByDay),
		"first_message_time": result.FirstMessageTime,
		"last_message_time":  result.LastMessageTime,
		"search_path":        result.Path,
		"query_since":        unixOrZero(start),
		"query_until":        unixOrZero(end),
		"cache_mode":         "full",
		"by_type":            byType,
		"by_chat":            byChat,
		"by_day":             byDay,
	}
	s.setStatsCache(cacheKey, payload, 60*time.Second)
	c.Set(databaseQueryRowsContextKey, int(result.Total))
	c.Set(databaseQueryPathContextKey, result.Path)
	writeJSON(c, payload)
}

func keywordAnalyticsCacheKey(
	keyword string,
	chats []string,
	start time.Time,
	end time.Time,
	messageType int64,
	chatLimit int,
) string {
	return fmt.Sprintf(
		"analytics:v1:%s:%s:%d:%d:%d:%d",
		strings.ToLower(strings.TrimSpace(keyword)),
		strings.Join(chats, ","),
		unixOrZero(start),
		unixOrZero(end),
		messageType,
		chatLimit,
	)
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
