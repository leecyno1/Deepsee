package observability

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultLogCapacity = 1_000

type LogEntry struct {
	ID       uint64 `json:"id"`
	Time     string `json:"time"`
	Level    string `json:"level"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

type LogStore struct {
	mu       sync.RWMutex
	capacity int
	nextID   uint64
	entries  []LogEntry
}

type logLineWriter struct {
	mu      sync.Mutex
	pending []byte
	store   *LogStore
}

var (
	defaultLogs      = NewLogStore(defaultLogCapacity)
	defaultLogWriter = &logLineWriter{store: defaultLogs}
)

func NewLogStore(capacity int) *LogStore {
	if capacity <= 0 {
		capacity = 1
	}
	return &LogStore{capacity: capacity}
}

func Logs() *LogStore {
	return defaultLogs
}

func LogWriter() io.Writer {
	return defaultLogWriter
}

func (s *LogStore) Add(entry LogEntry) {
	if s == nil {
		return
	}
	entry.Level = normalizeLevel(entry.Level)
	entry.Category = normalizeCategory(entry.Category)
	entry.Message = strings.TrimSpace(entry.Message)
	if entry.Message == "" {
		return
	}
	if len(entry.Message) > 2_048 {
		entry.Message = entry.Message[:2_048] + "…"
	}
	if entry.Time == "" {
		entry.Time = time.Now().Format(time.RFC3339Nano)
	}

	s.mu.Lock()
	s.nextID++
	entry.ID = s.nextID
	s.entries = append(s.entries, entry)
	if overflow := len(s.entries) - s.capacity; overflow > 0 {
		copy(s.entries, s.entries[overflow:])
		s.entries = s.entries[:s.capacity]
	}
	s.mu.Unlock()
}

func (s *LogStore) Recent(category, level string, limit int) []LogEntry {
	if s == nil {
		return nil
	}
	category = strings.ToLower(strings.TrimSpace(category))
	level = strings.ToLower(strings.TrimSpace(level))
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]LogEntry, 0, min(limit, len(s.entries)))
	for index := len(s.entries) - 1; index >= 0 && len(result) < limit; index-- {
		entry := s.entries[index]
		if category != "" && category != "all" && entry.Category != category {
			continue
		}
		if level != "" && level != "all" && entry.Level != level {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func (s *LogStore) CategoryCounts() map[string]int {
	counts := map[string]int{
		"system":   0,
		"http":     0,
		"database": 0,
		"cache":    0,
		"media":    0,
		"push":     0,
	}
	if s == nil {
		return counts
	}
	s.mu.RLock()
	for _, entry := range s.entries {
		counts[entry.Category]++
	}
	s.mu.RUnlock()
	return counts
}

func (s *LogStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

func (s *LogStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries = nil
	s.mu.Unlock()
}

func (w *logLineWriter) Write(data []byte) (int, error) {
	if w == nil || w.store == nil {
		return len(data), nil
	}
	w.mu.Lock()
	w.pending = append(w.pending, data...)
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		line := append([]byte(nil), w.pending[:newline]...)
		w.pending = w.pending[newline+1:]
		w.store.ingest(line)
	}
	if len(w.pending) > 64*1024 {
		line := append([]byte(nil), w.pending...)
		w.pending = nil
		w.store.ingest(line)
	}
	w.mu.Unlock()
	return len(data), nil
}

func (s *LogStore) ingest(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	event := map[string]interface{}{}
	if err := json.Unmarshal(line, &event); err != nil {
		message := strings.TrimSpace(string(line))
		s.Add(LogEntry{Level: "info", Category: classifyLog(message, nil), Message: message})
		return
	}

	message := stringValue(event["message"])
	if message == "" {
		message = stringValue(event["error"])
	}
	if errorText := stringValue(event["error"]); errorText != "" && !strings.Contains(message, errorText) {
		if message == "" {
			message = errorText
		} else {
			message += ": " + errorText
		}
	}
	s.Add(LogEntry{
		Time:     eventTime(event["time"]),
		Level:    stringValue(event["level"]),
		Category: classifyLog(message, event),
		Message:  message,
	})
}

func eventTime(value interface{}) string {
	switch typed := value.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return parsed.Format(time.RFC3339Nano)
		}
		return typed
	case float64:
		seconds := int64(typed)
		nanos := int64((typed - float64(seconds)) * float64(time.Second))
		return time.Unix(seconds, nanos).Format(time.RFC3339Nano)
	case json.Number:
		if value, err := strconv.ParseInt(string(typed), 10, 64); err == nil {
			return time.Unix(value, 0).Format(time.RFC3339Nano)
		}
	}
	return time.Now().Format(time.RFC3339Nano)
}

func stringValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return string(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func normalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "debug":
		return "debug"
	case "warn", "warning":
		return "warn"
	case "error", "fatal", "panic":
		return "error"
	default:
		return "info"
	}
}

func normalizeCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "http", "database", "cache", "media", "push":
		return strings.ToLower(strings.TrimSpace(category))
	default:
		return "system"
	}
}

func classifyLog(message string, fields map[string]interface{}) string {
	var context strings.Builder
	context.WriteString(strings.ToLower(message))
	for _, key := range []string{"component", "module", "path", "group", "table"} {
		if value := stringValue(fields[key]); value != "" {
			context.WriteByte(' ')
			context.WriteString(strings.ToLower(value))
		}
	}
	text := context.String()
	switch {
	case containsAny(text, "/api/", "http ", "http server", "[gin]", "request"):
		return "http"
	case containsAny(text, "database", "sqlite", "wcdb", " sql", "query", "decrypt", "message change scan", "message shard"):
		return "database"
	case containsAny(text, "cache", "cached"):
		return "cache"
	case containsAny(text, "media", "image", "video", "voice", "sns", "ffmpeg"):
		return "media"
	case containsAny(text, "hook", "push", "hermes", "deliver", "webhook"):
		return "push"
	default:
		return "system"
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
