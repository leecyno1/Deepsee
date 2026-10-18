package messagehook

import (
	"strings"
	"time"
)

func (s *Service) RecentEvents(limit int) ([]Event, error) {
	if s == nil || s.store == nil {
		return []Event{}, nil
	}
	return s.store.RecentEvents(limit)
}

func (s *Service) ClearEvents() (int, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	return s.store.ClearEvents()
}

func (s *Service) CancelEvent(eventID, target string) (int64, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	affected, err := s.store.CancelEvent(eventID, target, time.Now())
	if err == nil && affected > 0 {
		s.cancelActiveDeliveries(strings.TrimSpace(eventID), strings.TrimSpace(target))
	}
	return affected, err
}

func (s *Service) RetryEvent(eventID, target string) (int64, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	affected, err := s.store.RetryEvent(eventID, target, time.Now())
	if err == nil && affected > 0 {
		s.wakeDeliveries()
	}
	return affected, err
}

func (s *Service) RetryEvents(eventIDs []string, target string) (int64, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	affected, err := s.store.RetryEvents(eventIDs, target, time.Now())
	if err == nil && affected > 0 {
		s.wakeDeliveries()
	}
	return affected, err
}

func (s *Service) DeleteEvent(eventID, target string) (int64, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	eventID = strings.TrimSpace(eventID)
	target = strings.TrimSpace(target)
	s.cancelActiveDeliveries(eventID, target)
	return s.store.DeleteEvent(eventID, target)
}

func (s *Service) DeleteEvents(eventIDs []string, target string) (int64, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	eventIDs, err := normalizeBatchEventIDs(eventIDs)
	if err != nil {
		return 0, err
	}
	target = strings.TrimSpace(target)
	for _, eventID := range eventIDs {
		s.cancelActiveDeliveries(eventID, target)
	}
	return s.store.DeleteEvents(eventIDs, target)
}

func (s *Service) Stats() (RuntimeStats, error) {
	var out RuntimeStats
	if s == nil || s.store == nil {
		return out, nil
	}
	storeStats, err := s.store.Stats()
	if err != nil {
		return out, err
	}
	out.HookStoreStats = storeStats
	s.statsMu.RLock()
	out.Running = true
	out.LastScanAt = s.lastScanAt
	out.LastScanDurationMS = s.lastScanDuration.Milliseconds()
	out.LastScanError = s.lastScanError
	out.ScannedSessions = s.scannedSessions
	out.ScannedMessages = s.scannedMessages
	out.MatchedMessages = s.matchedMessages
	s.statsMu.RUnlock()
	return out, nil
}

func (s *Service) StorePath() string {
	if s == nil || s.store == nil {
		return ""
	}
	return s.store.Path()
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	select {
	case <-s.runDone:
	case <-time.After(10 * time.Second):
		return &closeTimeoutError{path: s.StorePath()}
	}
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

type closeTimeoutError struct {
	path string
}

func (e *closeTimeoutError) Error() string {
	if strings.TrimSpace(e.path) == "" {
		return "message hook shutdown timed out"
	}
	return "message hook shutdown timed out: " + e.path
}
