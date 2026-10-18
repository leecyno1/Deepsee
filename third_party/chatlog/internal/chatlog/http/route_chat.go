package http

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/pkg/util"
)

func (s *Service) initChatRoutes(api *gin.RouterGroup) {
	api.GET("/sessions", s.handleSessions)
	api.GET("/history", s.handleHistory)
	api.GET("/search", s.handleSearch)
	api.GET("/unread", s.handleUnread)
	api.GET("/members", s.handleMembers)
	api.GET("/new_messages", s.handleNewMessages)
	api.GET("/stats", s.handleStats)
	api.GET("/analytics/keywords", s.handleKeywordAnalytics)
}

func (s *Service) handleSessions(c *gin.Context) {
	q := struct {
		Query string `form:"query"`
		Limit int    `form:"limit"`
	}{}
	if err := c.ShouldBindQuery(&q); err != nil {
		errors.Err(c, errors.InvalidArg("query"))
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 20, 1, 500)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	offset, err := parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}
	// SessionV4.NickName is the last sender display name, not the session
	// display name. Resolve contacts/rooms before filtering so the API's
	// "query" parameter really searches what the web console renders.
	sessions, err := s.db.GetSessions("", 0, 0)
	if err != nil {
		errors.Err(c, err)
		return
	}
	queryText := strings.ToLower(strings.TrimSpace(q.Query))
	items := make([]gin.H, 0, min(limit, len(sessions.Items)))
	matched := 0
	for _, sess := range sessions.Items {
		chatType := classifyChatType(sess.UserName)
		isGroup := chatType == "group"
		chat := sess.UserName
		if isGroup {
			if room, _ := s.db.GetChatRoom(sess.UserName); room != nil {
				if display := strings.TrimSpace(room.DisplayName()); display != "" {
					chat = display
				}
			}
		} else {
			if contact, _ := s.db.GetContact(sess.UserName); contact != nil {
				if display := strings.TrimSpace(contact.DisplayName()); display != "" {
					chat = display
				}
			}
		}
		if chat == "" {
			chat = sess.UserName
		}
		if queryText != "" {
			haystack := strings.ToLower(strings.Join([]string{
				sess.UserName,
				chat,
				sess.NickName,
				sess.Content,
			}, "\n"))
			if !strings.Contains(haystack, queryText) {
				continue
			}
		}
		matched++
		if matched <= offset || len(items) >= limit {
			continue
		}
		lastSender := strings.TrimSpace(sess.LastSenderDisplayName)
		if lastSender == "" {
			lastSender = strings.TrimSpace(sess.LastMsgSender)
		}
		items = append(items, gin.H{
			"chat":              chat,
			"username":          sess.UserName,
			"is_group":          isGroup,
			"chat_type":         chatType,
			"unread":            sess.UnreadCount,
			"last_msg_type":     formatMessageType(sess.LastMsgType),
			"last_msg_sub_type": sess.LastMsgSubType,
			"last_sender":       lastSender,
			"summary":           sess.Content,
			"timestamp":         sess.NTime.Unix(),
			"time":              sess.NTime.Format("01-02 15:04"),
		})
	}
	writeJSON(c, gin.H{"sessions": items, "total": matched, "offset": offset,
		"has_more": offset+len(items) < matched})
}

func (s *Service) handleHistory(c *gin.Context) {
	q := struct {
		Chat     string `form:"chat"`
		Sender   string `form:"sender"`
		Keyword  string `form:"keyword"`
		Time     string `form:"time"`
		Since    string `form:"since"`
		Until    string `form:"until"`
		MsgType  int64  `form:"msg_type"`
		SubType  int64  `form:"sub_type"`
		Hour     string `form:"hour"`
		IsSelf   string `form:"is_self"`
		HasMedia string `form:"has_media"`
		Limit    int    `form:"limit"`
		Offset   int    `form:"offset"`
	}{}
	if err := c.ShouldBindQuery(&q); err != nil {
		errors.Err(c, errors.InvalidArg("query"))
		return
	}
	if strings.TrimSpace(q.Chat) == "" {
		errors.Err(c, errors.InvalidArg("chat"))
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 50, 1, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	q.Limit = limit
	q.Offset, err = parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}
	hour, err := parseOptionalHour(q.Hour)
	if err != nil {
		errors.Err(c, err)
		return
	}
	isSelfFilter, err := parseOptionalBool(q.IsSelf)
	if err != nil {
		errors.Err(c, errors.InvalidArg("is_self"))
		return
	}
	hasMediaFilter, err := parseOptionalBool(q.HasMedia)
	if err != nil {
		errors.Err(c, errors.InvalidArg("has_media"))
		return
	}
	start, end, _, err := parseSinceUntil(q.Time, q.Since, q.Until)
	if err != nil {
		errors.Err(c, err)
		return
	}
	keyword := strings.TrimSpace(q.Keyword)
	if keyword != "" {
		keyword = regexp.QuoteMeta(keyword)
	}
	// Fetch the full matching set before counting and pagination. The old
	// limit+offset prefetch paginated an ascending slice from its oldest edge,
	// which made page 2 repeat page 1 and made total_count a window size.
	messages, err := s.db.GetMessages(start, end, q.Chat, strings.TrimSpace(q.Sender), keyword, 0, 0)
	if err != nil {
		errors.Err(c, err)
		return
	}
	messages = filterHistoryMessages(messages, q.MsgType, q.SubType, hour, isSelfFilter, hasMediaFilter)
	totalCount := len(messages)
	messages = paginateNewestMessages(messages, q.Limit, q.Offset)
	chat := q.Chat
	username := q.Chat
	if len(messages) > 0 {
		if messages[0].TalkerName != "" {
			chat = messages[0].TalkerName
		}
		if messages[0].Talker != "" {
			username = messages[0].Talker
		}
	}
	chatType := classifyChatType(username)
	isGroup := chatType == "group"
	rows := make([]gin.H, 0, len(messages))
	// Keep media key/path cache warm for direct /image/{md5} access.
	s.populateMD5PathCache(messages)
	s.enrichMessages(messages)
	for _, m := range messages {
		rows = append(rows, toHistoryMessage(m, c.Request.Host))
	}
	writeJSON(c, historyResponse{
		Chat:       chat,
		UserName:   username,
		IsGroup:    isGroup,
		ChatType:   chatType,
		TotalCount: totalCount,
		Count:      len(rows),
		Limit:      q.Limit,
		Offset:     q.Offset,
		Messages:   toHistoryMessageOutList(rows),
	})
}

func (s *Service) handleSearch(c *gin.Context) {
	q := struct {
		Keyword string `form:"keyword"`
		Chats   string `form:"chats"`
		Time    string `form:"time"`
		Since   string `form:"since"`
		Until   string `form:"until"`
		MsgType int64  `form:"msg_type"`
		Limit   int    `form:"limit"`
		Offset  int    `form:"offset"`
		Match   string `form:"match"`
		Sort    string `form:"sort"`
		Cursor  string `form:"cursor"`
	}{}
	if err := c.ShouldBindQuery(&q); err != nil {
		errors.Err(c, errors.InvalidArg("query"))
		return
	}
	if strings.TrimSpace(q.Keyword) == "" {
		errors.Err(c, errors.InvalidArg("keyword"))
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 20, 1, 500)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	q.Limit = limit
	q.Offset, err = parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 5000)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}
	q.Match = strings.ToLower(strings.TrimSpace(q.Match))
	if q.Match == "" {
		q.Match = "phrase"
	}
	if q.Match != "phrase" && q.Match != "all" && q.Match != "any" {
		errors.Err(c, errors.InvalidArg("match"))
		return
	}
	q.Sort = strings.ToLower(strings.TrimSpace(q.Sort))
	if q.Sort == "" {
		q.Sort = "time"
	}
	if q.Sort != "time" && q.Sort != "relevance" {
		errors.Err(c, errors.InvalidArg("sort"))
		return
	}
	if strings.TrimSpace(q.Cursor) != "" && q.Offset != 0 {
		errors.Err(c, errors.InvalidArg("cursor or offset"))
		return
	}
	start, end, _, err := parseSinceUntil(q.Time, q.Since, q.Until)
	if err != nil {
		errors.Err(c, err)
		return
	}

	chats := util.Str2List(q.Chats, ",")
	searchScopeChats := append([]string(nil), chats...)
	if len(chats) == 0 {
		sessions, err := s.db.GetSessions("", 0, 0)
		if err == nil {
			for _, sess := range sessions.Items {
				chats = append(chats, sess.UserName)
			}
		}
	}

	queryFingerprint := messageSearchQueryFingerprint(
		q.Keyword,
		q.Match,
		q.Sort,
		searchScopeChats,
		start,
		end,
		q.MsgType,
	)
	cursor, err := decodeMessageSearchCursor(q.Cursor, queryFingerprint)
	if err != nil {
		errors.Err(c, errors.InvalidArg("cursor"))
		return
	}
	if searcher, ok := s.db.(AdvancedMessageSearchQuery); ok {
		result, searchErr := searcher.SearchMessagesAdvanced(c.Request.Context(), model.MessageSearchRequest{
			StartTime:   start,
			EndTime:     end,
			Talkers:     searchScopeChats,
			Keyword:     q.Keyword,
			MessageType: q.MsgType,
			Limit:       q.Limit,
			Offset:      q.Offset,
			Match:       q.Match,
			Sort:        q.Sort,
			Cursor:      cursor,
		})
		if searchErr == nil {
			s.populateMD5PathCache(result.Messages)
			s.enrichMessages(result.Messages)
			out := make([]gin.H, 0, len(result.Messages))
			for _, message := range result.Messages {
				row := toHistoryMessage(message, c.Request.Host)
				row["chat"] = message.TalkerName
				if row["chat"] == "" {
					row["chat"] = message.Talker
				}
				row["username"] = message.Talker
				row["relevance"] = result.Scores[model.MessageSearchResultKey(message)]
				row["match_terms"], row["highlights"] = searchHighlights(message.PlainTextContent(), result.Terms)
				out = append(out, row)
			}
			nextCursor := ""
			if len(result.Messages) == q.Limit {
				last := result.Messages[len(result.Messages)-1]
				nextCursor = encodeMessageSearchCursor(queryFingerprint, model.MessageSearchCursor{
					Score:     result.Scores[model.MessageSearchResultKey(last)],
					Timestamp: last.Time.Unix(),
					Seq:       last.Seq,
					LocalID:   last.DBLocalID,
					Talker:    last.Talker,
				})
			}
			c.Set(databaseQueryRowsContextKey, len(out))
			c.Set(databaseQueryPathContextKey, result.Path)
			writeJSON(c, searchResponse{
				TotalCount: result.Total,
				Count:      len(out),
				Limit:      q.Limit,
				Offset:     q.Offset,
				SearchPath: result.Path,
				Match:      result.Match,
				Sort:       result.Sort,
				NextCursor: nextCursor,
				Messages:   toHistoryMessageOutList(out),
			})
			return
		}
	}

	if searcher, ok := s.db.(MessageSearchQuery); ok {
		messages, totalCount, searchPath, searchErr := searcher.SearchMessages(
			c.Request.Context(),
			start,
			end,
			searchScopeChats,
			q.Keyword,
			q.MsgType,
			q.Limit,
			q.Offset,
		)
		if searchErr == nil {
			s.populateMD5PathCache(messages)
			s.enrichMessages(messages)
			out := make([]gin.H, 0, len(messages))
			for _, message := range messages {
				row := toHistoryMessage(message, c.Request.Host)
				row["chat"] = message.TalkerName
				if row["chat"] == "" {
					row["chat"] = message.Talker
				}
				row["username"] = message.Talker
				out = append(out, row)
			}
			c.Set(databaseQueryRowsContextKey, len(out))
			c.Set(databaseQueryPathContextKey, searchPath)
			writeJSON(c, searchResponse{
				TotalCount: totalCount,
				Count:      len(out),
				Limit:      q.Limit,
				Offset:     q.Offset,
				SearchPath: searchPath,
				Messages:   toHistoryMessageOutList(out),
			})
			return
		}
	}

	out := make([]gin.H, 0, q.Limit+q.Offset)
	kwPattern := regexp.QuoteMeta(q.Keyword)
	for _, chat := range chats {
		// Keyword matching happens after message decoding, so limiting raw rows
		// before the filter can silently miss older matches. Scan each selected
		// chat fully so "search" and total_count retain their literal meaning.
		msgs, err := s.db.GetMessages(start, end, chat, "", kwPattern, 0, 0)
		if err != nil {
			continue
		}
		msgs = filterByMsgType(msgs, q.MsgType)
		s.populateMD5PathCache(msgs)
		s.enrichMessages(msgs)
		for _, m := range msgs {
			row := toHistoryMessage(m, c.Request.Host)
			row["chat"] = m.TalkerName
			if row["chat"] == "" {
				row["chat"] = m.Talker
			}
			row["username"] = m.Talker
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ti := toInt64(out[i]["timestamp"])
		tj := toInt64(out[j]["timestamp"])
		if ti == tj {
			return toInt64(out[i]["local_id"]) > toInt64(out[j]["local_id"])
		}
		return ti > tj
	})
	totalCount := len(out)
	out = paginateRows(out, q.Limit, q.Offset)
	c.Set(databaseQueryRowsContextKey, len(out))
	c.Set(databaseQueryPathContextKey, "ordinary_message_tables")
	writeJSON(c, searchResponse{
		TotalCount: totalCount,
		Count:      len(out),
		Limit:      q.Limit,
		Offset:     q.Offset,
		SearchPath: "ordinary_message_tables",
		Messages:   toHistoryMessageOutList(out),
	})
}

func classifyChatType(username string) string {
	u := strings.ToLower(strings.TrimSpace(username))
	switch {
	case strings.HasSuffix(u, "@chatroom"):
		return "group"
	case u == "brandsessionholder", u == "brandprivatemsg@hardcode":
		// WeChat 4.x uses BrandSessionHolder as the folded public-account
		// container. Its messages live under individual gh_* talkers in
		// biz_message_N.db, so a direct Msg_<holder-md5> lookup is empty.
		return "folded"
	case strings.HasPrefix(u, "gh_"):
		return "official_account"
	case strings.HasPrefix(u, "notifymessage"), strings.HasPrefix(u, "notification_messages"), strings.HasPrefix(u, "floatbottle"):
		return "folded"
	default:
		return "private"
	}
}

func parseFilterSet(c *gin.Context) (map[string]bool, error) {
	parts := make([]string, 0, 4)
	parts = append(parts, c.QueryArray("filter")...)
	if v := strings.TrimSpace(c.Query("filter")); v != "" {
		parts = append(parts, util.Str2List(v, ",")...)
	}
	set := map[string]bool{}
	for _, p := range parts {
		x := strings.ToLower(strings.TrimSpace(p))
		switch x {
		case "", "all":
			return nil, nil
		case "private":
			set["private"] = true
		case "group":
			set["group"] = true
		case "official", "official_account":
			set["official_account"] = true
		case "folded", "fold":
			set["folded"] = true
		default:
			return nil, errors.InvalidArg("filter")
		}
	}
	if len(set) == 0 {
		return nil, nil
	}
	return set, nil
}

func (s *Service) findDBFile(group string, preferContains ...string) (string, error) {
	if s.db == nil || !s.db.Ready() {
		return "", fmt.Errorf("database not ready")
	}
	dbs, err := s.db.GetDecryptedDBs()
	if err != nil {
		return "", err
	}
	files := dbs[strings.ToLower(group)]
	if len(files) == 0 {
		return "", fmt.Errorf("%s database not found", group)
	}
	for _, prefer := range preferContains {
		for _, f := range files {
			if strings.Contains(strings.ToLower(filepath.Base(f)), strings.ToLower(prefer)) {
				return f, nil
			}
		}
	}
	return files[0], nil
}

func (s *Service) handleUnread(c *gin.Context) {
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 20, 1, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	filterSet, err := parseFilterSet(c)
	if err != nil {
		errors.Err(c, err)
		return
	}

	file, err := s.findDBFile("session", "session.db")
	if err != nil {
		errors.Err(c, err)
		return
	}
	sql := `SELECT username, unread_count, summary, last_timestamp, last_msg_type, last_msg_sender, last_sender_display_name
FROM SessionTable
WHERE unread_count > 0
ORDER BY last_timestamp DESC`
	rows, err := s.db.ExecuteSQL("session", file, sql)
	if err != nil {
		errors.Err(c, err)
		return
	}

	out := make([]gin.H, 0, min(limit, len(rows)))
	total := 0
	for _, row := range rows {
		username := toString(row["username"])
		chatType := classifyChatType(username)
		if filterSet != nil && !filterSet[chatType] {
			continue
		}
		total++
		if len(out) >= limit {
			continue
		}
		display := username
		if contact, _ := s.db.GetContact(username); contact != nil {
			display = contact.DisplayName()
		} else if room, _ := s.db.GetChatRoom(username); room != nil {
			display = room.DisplayName()
		}
		ts := toInt64(row["last_timestamp"])
		lastSender := toString(row["last_sender_display_name"])
		if lastSender == "" {
			lastSender = toString(row["last_msg_sender"])
		}
		out = append(out, gin.H{
			"chat":          display,
			"username":      username,
			"is_group":      chatType == "group",
			"chat_type":     chatType,
			"unread":        toInt64(row["unread_count"]),
			"last_msg_type": formatMessageType(toInt64(row["last_msg_type"])),
			"last_sender":   lastSender,
			"summary":       toString(row["summary"]),
			"timestamp":     ts,
			"time":          time.Unix(ts, 0).Format("01-02 15:04"),
		})
	}
	writeJSON(c, gin.H{"sessions": out, "total": total})
}

func (s *Service) handleMembers(c *gin.Context) {
	chat := strings.TrimSpace(c.Query("chat"))
	if chat == "" {
		errors.Err(c, errors.InvalidArg("chat"))
		return
	}
	room, err := s.db.GetChatRoom(chat)
	if err != nil {
		errors.Err(c, err)
		return
	}
	members := make([]gin.H, 0, len(room.Users))
	for _, u := range room.Users {
		display := room.User2DisplayName[u.UserName]
		if display == "" {
			display = u.UserName
		}
		members = append(members, gin.H{
			"username": u.UserName,
			"display":  display,
			"is_owner": room.Owner != "" && room.Owner == u.UserName,
		})
	}
	writeJSON(c, gin.H{
		"chat":     room.DisplayName(),
		"username": room.Name,
		"count":    len(members),
		"members":  members,
	})
}

func (s *Service) handleStats(c *gin.Context) {
	chat := strings.TrimSpace(c.Query("chat"))
	if chat == "" {
		errors.Err(c, errors.InvalidArg("chat"))
		return
	}
	start, end, _, err := parseSinceUntil(c.Query("time"), c.Query("since"), c.Query("until"))
	if err != nil {
		errors.Err(c, err)
		return
	}
	force := strings.EqualFold(strings.TrimSpace(c.Query("force")), "true") ||
		strings.TrimSpace(c.Query("force")) == "1"
	payload, err := s.chatStats(chat, strings.TrimSpace(c.Query("time")), start, end, force)
	if err != nil {
		errors.Err(c, err)
		return
	}
	writeJSON(c, payload)
}
