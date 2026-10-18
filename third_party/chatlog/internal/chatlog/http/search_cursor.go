package http

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sjzar/chatlog/internal/model"
)

type messageSearchCursorToken struct {
	Query  string                    `json:"q"`
	Cursor model.MessageSearchCursor `json:"c"`
}

type searchHighlightRange struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Term  string `json:"term"`
}

func encodeMessageSearchCursor(query string, cursor model.MessageSearchCursor) string {
	payload, err := json.Marshal(messageSearchCursorToken{Query: query, Cursor: cursor})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeMessageSearchCursor(token, query string) (*model.MessageSearchCursor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid search cursor")
	}
	var decoded messageSearchCursorToken
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Query != query {
		return nil, fmt.Errorf("search cursor does not match query")
	}
	return &decoded.Cursor, nil
}

func messageSearchQueryFingerprint(
	keyword, match, sortMode string,
	chats []string,
	start, end time.Time,
	messageType int64,
) string {
	scope := append([]string(nil), chats...)
	sort.Strings(scope)
	parts := []string{
		strings.TrimSpace(keyword),
		strings.ToLower(strings.TrimSpace(match)),
		strings.ToLower(strings.TrimSpace(sortMode)),
		strings.Join(scope, "\x1f"),
		strconv.FormatInt(start.Unix(), 10),
		strconv.FormatInt(end.Unix(), 10),
		strconv.FormatInt(messageType, 10),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}

func searchHighlights(content string, terms []string) ([]string, []searchHighlightRange) {
	contentRunes := []rune(content)
	lower := strings.ToLower(content)
	matched := make([]string, 0, len(terms))
	ranges := make([]searchHighlightRange, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		byteIndex := strings.Index(lower, strings.ToLower(term))
		if byteIndex < 0 {
			continue
		}
		start := utf8.RuneCountInString(content[:byteIndex])
		end := start + utf8.RuneCountInString(term)
		if end > len(contentRunes) {
			end = len(contentRunes)
		}
		matched = append(matched, term)
		ranges = append(ranges, searchHighlightRange{Start: start, End: end, Term: term})
	}
	return matched, ranges
}
