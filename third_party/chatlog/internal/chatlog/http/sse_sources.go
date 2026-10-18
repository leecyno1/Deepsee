package http

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
)

const sseSampleInterval = 2 * time.Second

func (s *Service) startEventStream() {
	if s == nil {
		return
	}
	s.eventMu.Lock()
	if s.eventCancel != nil {
		s.eventMu.Unlock()
		return
	}
	if s.events == nil || s.events.isClosed() {
		s.events = newSSEHub()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.eventCancel = cancel
	s.eventDone = make(chan struct{})
	done := s.eventDone
	s.eventMu.Unlock()

	if feed, ok := s.db.(ports.MessageChangeFeed); ok {
		unsubscribe, err := feed.SubscribeMessageChanges("http-sse", time.Now(), s.publishMessageChange)
		if err != nil {
			log.Warn().Err(err).Msg("SSE message-change bridge is unavailable")
		} else {
			s.eventMu.Lock()
			s.eventMessageCancel = unsubscribe
			s.eventMu.Unlock()
		}
	}
	go s.runEventSampler(ctx, done)
}

func (s *Service) stopEventStream() {
	if s == nil {
		return
	}
	s.eventMu.Lock()
	cancel := s.eventCancel
	done := s.eventDone
	unsubscribe := s.eventMessageCancel
	s.eventCancel = nil
	s.eventDone = nil
	s.eventMessageCancel = nil
	hub := s.events
	s.eventMu.Unlock()
	if hub != nil {
		hub.close()
	}
	if unsubscribe != nil {
		unsubscribe()
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
}

func (s *Service) wakeEventStream() {
	if s != nil && s.events != nil {
		s.events.notify()
	}
}

func (s *Service) publishMessageChange(_ context.Context, batch ports.MessageChangeBatch) error {
	if s == nil || s.events == nil {
		return nil
	}
	return s.events.publish("message", "message", map[string]interface{}{
		"sequence":    batch.Sequence,
		"talker":      batch.Talker,
		"count":       len(batch.Messages),
		"detected_at": batch.DetectedAt,
	}, false)
}

func (s *Service) runEventSampler(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(sseSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleEventTopics(ctx)
		case <-s.events.wake:
			s.sampleEventTopics(ctx)
		}
	}
}

func (s *Service) sampleEventTopics(ctx context.Context) {
	if s == nil || s.events == nil {
		return
	}
	topics := s.events.activeTopics()
	if _, ok := topics["runtime"]; ok {
		_ = s.events.publish("runtime", "runtime", s.runtimeStatusSnapshot(time.Now()), false)
	}
	if _, ok := topics["log"]; ok {
		payload := s.runtimeLogsSnapshot("all", "all", 500)
		delete(payload, "timestamp")
		_ = s.events.publish("log", "log", payload, true)
	}
	if _, ok := topics["hook"]; ok {
		if payload, err := s.hookStatusSnapshot(); err == nil {
			_ = s.events.publish("hook", "hook_status", payload, true)
		}
		if payload, err := s.hookEventsSnapshot(50); err == nil {
			_ = s.events.publish("hook", "hook_events", payload, true)
		}
	}
	if _, ok := topics["ocr"]; ok {
		statusCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		status := s.ocrStatus(statusCtx)
		cancel()
		// Probe timestamps are operational noise; readiness/PIDs/errors carry the
		// actual state and keep idle OCR streams silent.
		status.LocalService.LastCheckAt = 0
		_ = s.events.publish("ocr", "ocr_status", status, true)
	}
}
