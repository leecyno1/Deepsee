package http

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	sseReplayLimit     = 256
	sseClientQueueSize = 64
	sseHeartbeat       = 20 * time.Second
)

var sseTopicNames = map[string]struct{}{
	"runtime": {},
	"hook":    {},
	"ocr":     {},
	"log":     {},
	"message": {},
}

type sseEvent struct {
	ID    uint64
	Topic string
	Name  string
	Data  []byte
}

type sseSubscriber struct {
	topics map[string]struct{}
	events chan sseEvent
}

type sseHub struct {
	mu           sync.Mutex
	nextID       uint64
	nextClient   uint64
	clients      map[uint64]*sseSubscriber
	replay       []sseEvent
	fingerprints map[string][sha256.Size]byte
	wake         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
	closed       bool
}

func newSSEHub() *sseHub {
	return &sseHub{
		clients:      make(map[uint64]*sseSubscriber),
		fingerprints: make(map[string][sha256.Size]byte),
		wake:         make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
}

func (h *sseHub) close() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
		close(h.done)
	})
}

func (h *sseHub) isClosed() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

func (h *sseHub) notify() {
	if h == nil {
		return
	}
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *sseHub) publish(topic, name string, payload interface{}, onlyIfChanged bool) error {
	if h == nil {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	h.mu.Lock()
	fingerprint := sha256.Sum256(data)
	if onlyIfChanged && h.fingerprints[name] == fingerprint {
		h.mu.Unlock()
		return nil
	}
	h.fingerprints[name] = fingerprint
	h.nextID++
	event := sseEvent{ID: h.nextID, Topic: topic, Name: name, Data: data}
	h.replay = append(h.replay, event)
	if overflow := len(h.replay) - sseReplayLimit; overflow > 0 {
		copy(h.replay, h.replay[overflow:])
		h.replay = h.replay[:sseReplayLimit]
	}
	for _, client := range h.clients {
		if _, ok := client.topics[topic]; !ok {
			continue
		}
		// State streams prefer the latest state over unbounded per-client memory.
		select {
		case client.events <- event:
		default:
			select {
			case <-client.events:
			default:
			}
			select {
			case client.events <- event:
			default:
			}
		}
	}
	h.mu.Unlock()
	return nil
}

func (h *sseHub) subscribe(topics map[string]struct{}, lastEventID uint64) (uint64, <-chan sseEvent, func()) {
	h.mu.Lock()
	h.nextClient++
	id := h.nextClient
	client := &sseSubscriber{topics: topics, events: make(chan sseEvent, sseClientQueueSize)}
	if lastEventID > 0 {
		replayStart := len(h.replay) - sseClientQueueSize
		if replayStart < 0 {
			replayStart = 0
		}
		for _, event := range h.replay[replayStart:] {
			if event.ID <= lastEventID {
				continue
			}
			if _, ok := topics[event.Topic]; ok {
				client.events <- event
			}
		}
	}
	h.clients[id] = client
	h.mu.Unlock()
	h.notify()
	return id, client.events, func() {
		h.mu.Lock()
		delete(h.clients, id)
		h.mu.Unlock()
		h.notify()
	}
}

func (h *sseHub) activeTopics() map[string]struct{} {
	result := make(map[string]struct{})
	if h == nil {
		return result
	}
	h.mu.Lock()
	for _, client := range h.clients {
		for topic := range client.topics {
			result[topic] = struct{}{}
		}
	}
	h.mu.Unlock()
	return result
}

func parseSSETopics(raw string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, value := range strings.Split(raw, ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if _, ok := sseTopicNames[value]; ok {
			result[value] = struct{}{}
		}
	}
	if len(result) == 0 {
		for value := range sseTopicNames {
			result[value] = struct{}{}
		}
	}
	return result
}

func parseLastEventID(c *gin.Context) uint64 {
	raw := strings.TrimSpace(c.GetHeader("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(c.Query("last_event_id"))
	}
	value, _ := strconv.ParseUint(raw, 10, 64)
	return value
}

func writeSSEEvent(c *gin.Context, event sseEvent) error {
	_, err := c.Writer.Write([]byte("id: " + strconv.FormatUint(event.ID, 10) + "\n" +
		"event: " + event.Name + "\n" +
		"data: " + string(event.Data) + "\n\n"))
	if err == nil {
		c.Writer.Flush()
	}
	return err
}

func (s *Service) handleEventStream(c *gin.Context) {
	s.eventMu.Lock()
	hub := s.events
	s.eventMu.Unlock()
	if hub == nil || hub.isClosed() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "event stream is not ready"})
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write([]byte("retry: 2000\n\n"))
	c.Writer.Flush()

	_, events, unsubscribe := hub.subscribe(parseSSETopics(c.Query("topics")), parseLastEventID(c))
	defer unsubscribe()
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-hub.done:
			return
		case event := <-events:
			if err := writeSSEEvent(c, event); err != nil {
				return
			}
		case now := <-heartbeat.C:
			if _, err := c.Writer.Write([]byte(": heartbeat " + now.Format(time.RFC3339Nano) + "\n\n")); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}
