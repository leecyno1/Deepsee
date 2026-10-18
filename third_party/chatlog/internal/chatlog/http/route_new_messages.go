package http

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
)

func (s *Service) handleNewMessages(c *gin.Context) {
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 200, 1, 5000)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	// Prefer a header so per-chat cursor keys do not leak into access logs,
	// browser history, or copied URLs. Keep the query value for compatibility
	// with existing API clients.
	rawState := strings.TrimSpace(c.GetHeader("X-Chatlog-State"))
	if rawState == "" {
		rawState = c.Query("state")
	}
	state, err := parseNewMessageState(rawState)
	if err != nil {
		errors.Err(c, errors.InvalidArg("state"))
		return
	}
	now := time.Now().Unix()
	fallback := newMessageCursor{Timestamp: now - 24*3600}

	sessionItems := make([]*model.Session, 0, 500)
	for offset := 0; ; offset += 500 {
		sessions, queryErr := s.db.GetSessions("", 500, offset)
		if queryErr != nil {
			errors.Err(c, queryErr)
			return
		}
		sessionItems = append(sessionItems, sessions.Items...)
		if len(sessions.Items) < 500 {
			break
		}
	}
	newState := make(map[string]newMessageCursor, len(state)+len(sessionItems))
	for chat, cursor := range state {
		newState[chat] = cursor
	}
	changed := make([]*model.Session, 0, len(sessionItems))
	for _, sess := range sessionItems {
		ts := sess.NTime.Unix()
		cursor, ok := state[sess.UserName]
		if !ok {
			cursor = fallback
			if ts >= fallback.Timestamp {
				newState[sess.UserName] = cursor
			}
		}
		// Equal-second sessions must still be checked: more than one message
		// can share create_time and the cursor also carries db_local_id.
		if ts >= cursor.Timestamp {
			changed = append(changed, sess)
		}
	}
	if len(changed) == 0 {
		writeJSON(c, gin.H{
			"count":          0,
			"messages":       []gin.H{},
			"new_state":      encodeNewMessageState(newState),
			"has_more":       false,
			"partial_errors": 0,
		})
		return
	}

	candidates := make([]*model.Message, 0, limit*2)
	partialErrors := 0
	hasMore := false
	for _, sess := range changed {
		cursor, ok := state[sess.UserName]
		if !ok {
			cursor = fallback
		}
		// The cursor API returns the oldest rows after the cursor. Reading one
		// extra row detects backlog without retaining the full pending range.
		msgs, err := s.db.GetMessagesAfter(sess.UserName, model.MessageCursor{
			Timestamp: cursor.Timestamp,
			LocalID:   cursor.LocalID,
		}, limit+1)
		if err != nil {
			partialErrors++
			continue
		}
		if len(msgs) > limit {
			hasMore = true
		}
		for _, m := range msgs {
			if messageAfterNewCursor(m, cursor) {
				candidates = append(candidates, m)
			}
		}
		if len(candidates) > limit {
			var trimmed bool
			candidates, trimmed = takeNewMessageBatch(candidates, limit)
			hasMore = hasMore || trimmed
		}
	}

	var trimmed bool
	candidates, trimmed = takeNewMessageBatch(candidates, limit)
	hasMore = hasMore || trimmed

	s.populateMD5PathCache(candidates)
	s.enrichMessages(candidates)
	out := make([]gin.H, 0, len(candidates))
	for _, m := range candidates {
		cursor := newMessageCursor{Timestamp: m.Time.Unix(), LocalID: m.DBLocalID}
		if current, ok := newState[m.Talker]; !ok || cursor.After(current) {
			newState[m.Talker] = cursor
		}
		row := toHistoryMessage(m, c.Request.Host)
		row["chat"] = m.TalkerName
		if row["chat"] == "" {
			row["chat"] = m.Talker
		}
		row["username"] = m.Talker
		row["is_group"] = m.IsChatRoom
		row["chat_type"] = classifyChatType(m.Talker)
		out = append(out, row)
	}

	writeJSON(c, gin.H{
		"count":          len(out),
		"messages":       out,
		"new_state":      encodeNewMessageState(newState),
		"has_more":       hasMore,
		"partial_errors": partialErrors,
	})
}

func takeNewMessageBatch(candidates []*model.Message, limit int) ([]*model.Message, bool) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Time.Unix() != candidates[j].Time.Unix() {
			return candidates[i].Time.Before(candidates[j].Time)
		}
		if candidates[i].DBLocalID != candidates[j].DBLocalID {
			return candidates[i].DBLocalID < candidates[j].DBLocalID
		}
		return candidates[i].Talker < candidates[j].Talker
	})
	hasMore := len(candidates) > limit
	if hasMore {
		candidates = candidates[:limit]
	}
	return candidates, hasMore
}

type newMessageCursor struct {
	Timestamp int64
	LocalID   int64
}

func (c newMessageCursor) After(other newMessageCursor) bool {
	return c.Timestamp > other.Timestamp || (c.Timestamp == other.Timestamp && c.LocalID > other.LocalID)
}

func messageAfterNewCursor(message *model.Message, cursor newMessageCursor) bool {
	if message == nil {
		return false
	}
	return newMessageCursor{Timestamp: message.Time.Unix(), LocalID: message.DBLocalID}.After(cursor)
}

func parseNewMessageState(raw string) (map[string]newMessageCursor, error) {
	out := map[string]newMessageCursor{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	for chat, encoded := range values {
		chat = strings.TrimSpace(chat)
		if chat == "" {
			return nil, fmt.Errorf("empty chat cursor")
		}
		var cursorText string
		if err := json.Unmarshal(encoded, &cursorText); err == nil {
			parts := strings.Split(cursorText, ":")
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid cursor")
			}
			timestamp, tsErr := strconv.ParseInt(parts[0], 10, 64)
			localID, idErr := strconv.ParseInt(parts[1], 10, 64)
			if tsErr != nil || idErr != nil || timestamp < 0 || localID < 0 {
				return nil, fmt.Errorf("invalid cursor")
			}
			out[chat] = newMessageCursor{Timestamp: timestamp, LocalID: localID}
			continue
		}

		// Legacy state values were Unix seconds and implied that the whole
		// second had already been consumed.
		var timestamp int64
		if err := json.Unmarshal(encoded, &timestamp); err != nil || timestamp < 0 {
			return nil, fmt.Errorf("invalid legacy cursor")
		}
		out[chat] = newMessageCursor{Timestamp: timestamp, LocalID: int64(1<<63 - 1)}
	}
	return out, nil
}

func encodeNewMessageState(state map[string]newMessageCursor) map[string]string {
	out := make(map[string]string, len(state))
	for chat, cursor := range state {
		out[chat] = fmt.Sprintf("%d:%d", cursor.Timestamp, cursor.LocalID)
	}
	return out
}
