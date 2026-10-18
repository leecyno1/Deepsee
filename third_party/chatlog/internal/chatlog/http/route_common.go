package http

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/pkg/util"
)

func (s *Service) NoRoute(c *gin.Context) {
	path := c.Request.URL.Path
	switch {
	case strings.HasPrefix(path, "/api"), strings.HasPrefix(path, "/static"):
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
	default:
		c.Header("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate, value")
		c.Redirect(http.StatusFound, "/")
	}
}

func formatMessageType(t int64) string {
	switch t {
	case model.MessageTypeText:
		return "text"
	case model.MessageTypeImage:
		return "image"
	case model.MessageTypeVoice:
		return "voice"
	case model.MessageTypeCard:
		return "card"
	case model.MessageTypeVideo:
		return "video"
	case model.MessageTypeAnimation:
		return "sticker"
	case model.MessageTypeLocation:
		return "location"
	case model.MessageTypeShare:
		return "share"
	case model.MessageTypeVOIP:
		return "voip"
	case model.MessageTypeSystem:
		return "system"
	case model.MessageTypeSystemNotification:
		return "system_notification"
	default:
		return strconv.FormatInt(t, 10)
	}
}

// handleOpenimCorp 给定 @openim 用户的 wxid，返回其企业名 + 企业 ID。
// GET /api/v1/openim/{wxid}/corp
// wxid 不存在 / 不是 openim / 没有企业信息一律返回 200 + 空字段，不报错。
func (s *Service) handleOpenimCorp(c *gin.Context) {
	wxid := strings.TrimSpace(c.Param("wxid"))
	if wxid == "" {
		errors.Err(c, errors.InvalidArg("wxid"))
		return
	}
	resp := gin.H{
		"wxid":      wxid,
		"corp_id":   "",
		"corp_name": "",
	}
	if !strings.HasSuffix(wxid, "@openim") {
		writeJSON(c, resp)
		return
	}
	contact, err := s.db.GetContact(wxid)
	if err != nil || contact == nil {
		writeJSON(c, resp)
		return
	}
	resp["corp_id"] = contact.CorpID
	resp["corp_name"] = contact.CorpName
	writeJSON(c, resp)
}

func writeJSON(c *gin.Context, payload interface{}) {
	c.JSON(http.StatusOK, payload)
}

func parseSinceUntil(qTime, qSince, qUntil string) (time.Time, time.Time, bool, error) {
	qTime = strings.TrimSpace(qTime)
	if qTime != "" {
		start, end, ok := util.TimeRangeOf(qTime)
		if !ok {
			return time.Time{}, time.Time{}, false, errors.InvalidArg("time")
		}
		return start, end, true, nil
	}

	var (
		start time.Time
		end   time.Time
		ok    bool
	)
	if strings.TrimSpace(qSince) != "" {
		ts, err := strconv.ParseInt(strings.TrimSpace(qSince), 10, 64)
		if err != nil {
			return time.Time{}, time.Time{}, false, errors.InvalidArg("since")
		}
		start = time.Unix(ts, 0)
		ok = true
	}
	if strings.TrimSpace(qUntil) != "" {
		ts, err := strconv.ParseInt(strings.TrimSpace(qUntil), 10, 64)
		if err != nil {
			return time.Time{}, time.Time{}, false, errors.InvalidArg("until")
		}
		end = time.Unix(ts, 0)
		ok = true
	}
	return start, end, ok, nil
}

func toInt64(v interface{}) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(t)), 10, 64)
		return n
	default:
		return 0
	}
}

func toString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(v)
	}
}

func appendUnique(list []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return list
	}
	for _, it := range list {
		if it == v {
			return list
		}
	}
	return append(list, v)
}

func extractMediaRef(m *model.Message) (mediaType string, keys []string) {
	if m == nil || m.Contents == nil {
		return "", nil
	}
	get := func(k string) string {
		v, ok := m.Contents[k]
		if !ok {
			return ""
		}
		return strings.TrimSpace(toString(v))
	}

	switch m.Type {
	case model.MessageTypeImage:
		mediaType = "image"
		keys = appendUnique(keys, get("md5"))
		keys = appendUnique(keys, get("path"))
	case model.MessageTypeVideo:
		mediaType = "video"
		keys = appendUnique(keys, get("md5"))
		keys = appendUnique(keys, get("rawmd5"))
		keys = appendUnique(keys, get("path"))
	case model.MessageTypeVoice:
		mediaType = "voice"
		serverID := get("voice")
		if serverID != "" && serverID != "0" {
			// A valid server ID uniquely identifies the voice. local_id is only
			// a compatibility fallback and can map to several unrelated rows.
			keys = appendUnique(keys, serverID)
		} else {
			keys = appendUnique(keys, get("voice_local_id"))
			if m.Seq > 0 {
				keys = appendUnique(keys, fmt.Sprintf("%d", m.Seq%1000000))
			}
		}
	case model.MessageTypeShare:
		if m.SubType == model.MessageSubTypeFile {
			mediaType = "file"
			keys = appendUnique(keys, get("md5"))
			keys = appendUnique(keys, get("path"))
		}
	}
	return mediaType, keys
}

func buildMediaPath(mediaType, key string) string {
	mediaType = strings.TrimSpace(mediaType)
	key = strings.TrimSpace(key)
	if mediaType == "" || key == "" {
		return ""
	}
	return "/" + mediaType + "/" + key
}

func filterByMsgType(messages []*model.Message, msgType int64) []*model.Message {
	if msgType == 0 {
		return messages
	}
	out := make([]*model.Message, 0, len(messages))
	for _, m := range messages {
		if m.Type == msgType {
			out = append(out, m)
		}
	}
	return out
}

func parseOptionalBool(raw string) (*bool, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return nil, nil
	}
	switch v {
	case "1", "true", "yes", "y":
		b := true
		return &b, nil
	case "0", "false", "no", "n":
		b := false
		return &b, nil
	default:
		return nil, errors.InvalidArg(raw)
	}
}

func parseOptionalHour(raw string) (int, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return -1, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1, errors.InvalidArg("hour")
	}
	if n < 0 || n > 23 {
		return -1, errors.InvalidArg("hour")
	}
	return n, nil
}

func filterHistoryMessages(messages []*model.Message, msgType, subType int64, hour int, isSelf, hasMedia *bool) []*model.Message {
	out := make([]*model.Message, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			continue
		}
		if msgType != 0 && m.Type != msgType {
			continue
		}
		if subType != 0 && m.SubType != subType {
			continue
		}
		if hour >= 0 && m.Time.Hour() != hour {
			continue
		}
		if isSelf != nil && m.IsSelf != *isSelf {
			continue
		}
		if hasMedia != nil {
			mediaType, keys := extractMediaRef(m)
			has := mediaType != "" && len(keys) > 0
			if has != *hasMedia {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// paginateNewestMessages paginates an ascending message slice from its newest
// end while preserving chronological order inside each page. Offset therefore
// means "skip this many newer messages", which is stable across page requests.
func paginateNewestMessages(messages []*model.Message, limit, offset int) []*model.Message {
	if offset < 0 {
		offset = 0
	}
	end := len(messages) - offset
	if end <= 0 {
		return []*model.Message{}
	}
	start := 0
	if limit > 0 && end > limit {
		start = end - limit
	}
	return messages[start:end]
}

func paginateRows(rows []gin.H, limit, offset int) []gin.H {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) {
		return []gin.H{}
	}
	if offset > 0 {
		rows = rows[offset:]
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

type historyMessageOut struct {
	Timestamp        int64                  `json:"timestamp"`
	Time             string                 `json:"time,omitempty"`
	Sender           string                 `json:"sender,omitempty"`
	SenderID         string                 `json:"sender_id,omitempty"`
	IsSelf           bool                   `json:"is_self"`
	Type             string                 `json:"type,omitempty"`
	TypeNum          int64                  `json:"type_num"`
	SubType          int64                  `json:"sub_type"`
	Content          string                 `json:"content,omitempty"`
	Contents         map[string]interface{} `json:"contents,omitempty"`
	AtUserList       []string               `json:"at_user_list,omitempty"`
	LocalID          int64                  `json:"local_id,omitempty"`
	DBLocalID        int64                  `json:"db_local_id"`
	MessageID        int64                  `json:"message_id"`
	ServerID         string                 `json:"server_id,omitempty"`
	MediaType        string                 `json:"media_type,omitempty"`
	MediaKey         string                 `json:"media_key,omitempty"`
	MediaKeys        []string               `json:"media_keys,omitempty"`
	MediaPath        string                 `json:"media_path,omitempty"`
	MediaURL         string                 `json:"media_url,omitempty"`
	ImageKey         string                 `json:"image_key,omitempty"`
	ImageKeys        []string               `json:"image_keys,omitempty"`
	ImagePath        string                 `json:"image_path,omitempty"`
	ImageURL         string                 `json:"image_url,omitempty"`
	ImageDescription string                 `json:"image_description,omitempty"`
	Chat             string                 `json:"chat,omitempty"`
	UserName         string                 `json:"username,omitempty"`
	IsGroup          bool                   `json:"is_group,omitempty"`
	ChatType         string                 `json:"chat_type,omitempty"`
	Relevance        int                    `json:"relevance,omitempty"`
	MatchTerms       []string               `json:"match_terms,omitempty"`
	Highlights       []searchHighlightRange `json:"highlights,omitempty"`
}

type historyResponse struct {
	Chat       string              `json:"chat,omitempty"`
	UserName   string              `json:"username,omitempty"`
	IsGroup    bool                `json:"is_group"`
	ChatType   string              `json:"chat_type,omitempty"`
	TotalCount int                 `json:"total_count"`
	Count      int                 `json:"count"`
	Limit      int                 `json:"limit,omitempty"`
	Offset     int                 `json:"offset,omitempty"`
	Messages   []historyMessageOut `json:"messages"`
}

type searchResponse struct {
	TotalCount int                 `json:"total_count"`
	Count      int                 `json:"count"`
	Limit      int                 `json:"limit,omitempty"`
	Offset     int                 `json:"offset,omitempty"`
	SearchPath string              `json:"search_path,omitempty"`
	Match      string              `json:"match,omitempty"`
	Sort       string              `json:"sort,omitempty"`
	NextCursor string              `json:"next_cursor,omitempty"`
	Messages   []historyMessageOut `json:"messages"`
}

type statsCountByType struct {
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

type statsCountBySender struct {
	Sender string `json:"sender"`
	Count  int64  `json:"count"`
}

type statsCountByHour struct {
	Hour  int `json:"hour"`
	Count int `json:"count"`
}

type statsResponse struct {
	Chat             string               `json:"chat"`
	UserName         string               `json:"username"`
	IsGroup          bool                 `json:"is_group"`
	ChatType         string               `json:"chat_type"`
	Total            int                  `json:"total"`
	SentCount        int                  `json:"sent_count"`
	ReceivedCount    int                  `json:"received_count"`
	ActiveSenders    int                  `json:"active_senders"`
	ActiveDays       int                  `json:"active_days"`
	FirstMessageTime int64                `json:"first_message_time"`
	LastMessageTime  int64                `json:"last_message_time"`
	QuerySince       int64                `json:"query_since"`
	QueryUntil       int64                `json:"query_until"`
	QueryRangeLabel  string               `json:"query_range_label"`
	CacheMode        string               `json:"cache_mode"`
	NewMessages      int                  `json:"new_messages"`
	WatermarkTime    int64                `json:"watermark_time"`
	GeneratedAt      int64                `json:"generated_at"`
	ByType           []statsCountByType   `json:"by_type"`
	TopSenders       []statsCountBySender `json:"top_senders"`
	ByHour           []statsCountByHour   `json:"by_hour"`
}

func toStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s := strings.TrimSpace(toString(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func toHistoryMessageOut(row gin.H) historyMessageOut {
	out := historyMessageOut{
		Timestamp:        toInt64(row["timestamp"]),
		Time:             strings.TrimSpace(toString(row["time"])),
		Sender:           strings.TrimSpace(toString(row["sender"])),
		SenderID:         strings.TrimSpace(toString(row["sender_id"])),
		IsSelf:           toInt64(row["is_self"]) == 1 || strings.EqualFold(strings.TrimSpace(toString(row["is_self"])), "true"),
		Type:             strings.TrimSpace(toString(row["type"])),
		TypeNum:          toInt64(row["type_num"]),
		SubType:          toInt64(row["sub_type"]),
		Content:          toString(row["content"]),
		Contents:         toStringInterfaceMap(row["contents"]),
		AtUserList:       toStringSlice(row["at_user_list"]),
		LocalID:          toInt64(row["local_id"]),
		DBLocalID:        toInt64(row["db_local_id"]),
		MessageID:        toInt64(row["message_id"]),
		ServerID:         strings.TrimSpace(toString(row["server_id"])),
		MediaType:        strings.TrimSpace(toString(row["media_type"])),
		MediaKey:         strings.TrimSpace(toString(row["media_key"])),
		MediaKeys:        toStringSlice(row["media_keys"]),
		MediaPath:        strings.TrimSpace(toString(row["media_path"])),
		MediaURL:         strings.TrimSpace(toString(row["media_url"])),
		ImageKey:         strings.TrimSpace(toString(row["image_key"])),
		ImageKeys:        toStringSlice(row["image_keys"]),
		ImagePath:        strings.TrimSpace(toString(row["image_path"])),
		ImageURL:         strings.TrimSpace(toString(row["image_url"])),
		ImageDescription: strings.TrimSpace(toString(row["image_description"])),
		Chat:             strings.TrimSpace(toString(row["chat"])),
		UserName:         strings.TrimSpace(toString(row["username"])),
		IsGroup:          toInt64(row["is_group"]) == 1 || strings.EqualFold(strings.TrimSpace(toString(row["is_group"])), "true"),
		ChatType:         strings.TrimSpace(toString(row["chat_type"])),
		Relevance:        int(toInt64(row["relevance"])),
		MatchTerms:       toStringSlice(row["match_terms"]),
	}
	if ranges, ok := row["highlights"].([]searchHighlightRange); ok {
		out.Highlights = ranges
	}
	return out
}

func toStringInterfaceMap(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed
	case gin.H:
		return map[string]interface{}(typed)
	default:
		return nil
	}
}

func toHistoryMessageOutList(rows []gin.H) []historyMessageOut {
	out := make([]historyMessageOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, toHistoryMessageOut(row))
	}
	return out
}

func toHistoryMessage(m *model.Message, host string) gin.H {
	if strings.TrimSpace(host) != "" {
		m.SetContent("host", host)
	}
	content := m.Content
	if content == "" {
		content = m.PlainTextContent()
	}
	sender := m.SenderName
	if sender == "" {
		sender = m.Sender
	}
	out := gin.H{
		"timestamp":    m.Time.Unix(),
		"time":         m.Time.Format("2006-01-02 15:04"),
		"sender":       sender,
		"sender_id":    m.Sender,
		"is_self":      m.IsSelf,
		"content":      content,
		"contents":     m.Contents,
		"at_user_list": m.AtUserList,
		"type":         formatMessageType(m.Type),
		"type_num":     m.Type,
		"sub_type":     m.SubType,
		"local_id":     m.ID,
		"db_local_id":  m.DBLocalID,
		"message_id":   m.ID,
	}
	if m.ServerID > 0 {
		out["server_id"] = strconv.FormatInt(m.ServerID, 10)
	}
	mediaType, mediaKeys := extractMediaRef(m)
	if mediaType != "" && len(mediaKeys) > 0 {
		mediaKey := mediaKeys[0]
		mediaPath := buildMediaPath(mediaType, mediaKey)
		out["media_type"] = mediaType
		out["media_key"] = mediaKey
		out["media_keys"] = mediaKeys
		out["media_path"] = mediaPath
		if strings.TrimSpace(host) != "" {
			out["media_url"] = "http://" + host + mediaPath
		}
		if mediaType == "image" {
			out["image_key"] = mediaKey
			out["image_keys"] = mediaKeys
			out["image_path"] = mediaPath
			if strings.TrimSpace(host) != "" {
				out["image_url"] = "http://" + host + mediaPath
			}
			if m.Contents != nil {
				if description := strings.TrimSpace(toString(m.Contents["image_description"])); description != "" {
					out["image_description"] = description
				}
			}
		}
	}
	return out
}
