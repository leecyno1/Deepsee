package http

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/model"
)

func (s *Service) currentOCRBackfillStatus(config conf.OCRConfig) imageOCRBackfillStatus {
	s.ocrBackfillMu.RLock()
	status := s.ocrBackfill
	s.ocrBackfillMu.RUnlock()
	status.Enabled = config.BackfillEnabled
	if status.StartedAt == 0 {
		status.ReceivedOnly = config.ReceivedOnly
		status.RespectScope = true
	}
	return status
}

func (s *Service) beginOCRBackfill(
	parent context.Context,
	limit int,
	receivedOnly bool,
	respectScope bool,
	talkers []string,
) (context.Context, error) {
	s.ocrBackfillMu.Lock()
	defer s.ocrBackfillMu.Unlock()
	if s.ocrBackfill.Running {
		return nil, errors.New("OCR history backfill is already running")
	}
	ctx, cancel := context.WithCancel(parent)
	s.ocrBackfillCancel = cancel
	s.ocrBackfillDone = make(chan struct{})
	s.ocrBackfill = imageOCRBackfillStatus{
		Enabled:      true,
		Running:      true,
		StartedAt:    time.Now().Unix(),
		Limit:        limit,
		ReceivedOnly: receivedOnly,
		RespectScope: respectScope,
		Talkers:      append([]string(nil), talkers...),
	}
	return ctx, nil
}

func (s *Service) updateOCRBackfillProgress(scanned, enqueued int) {
	s.ocrBackfillMu.Lock()
	s.ocrBackfill.Scanned = scanned
	s.ocrBackfill.Enqueued = enqueued
	s.ocrBackfillMu.Unlock()
}

func (s *Service) finishOCRBackfill(scanned, enqueued int, err error) {
	s.ocrBackfillMu.Lock()
	s.ocrBackfill.Running = false
	s.ocrBackfill.FinishedAt = time.Now().Unix()
	s.ocrBackfill.Scanned = scanned
	s.ocrBackfill.Enqueued = enqueued
	if err != nil && !errors.Is(err, context.Canceled) {
		s.ocrBackfill.Error = strings.TrimSpace(err.Error())
	} else if errors.Is(err, context.Canceled) {
		s.ocrBackfill.Error = "已停止"
	}
	s.ocrBackfillCancel = nil
	done := s.ocrBackfillDone
	s.ocrBackfillDone = nil
	s.ocrBackfillMu.Unlock()
	if done != nil {
		close(done)
	}
}

func (s *Service) cancelOCRBackfill() bool {
	s.ocrBackfillMu.RLock()
	cancel := s.ocrBackfillCancel
	done := s.ocrBackfillDone
	running := s.ocrBackfill.Running
	s.ocrBackfillMu.RUnlock()
	if cancel == nil || !running {
		return false
	}
	cancel()
	if done != nil {
		<-done
	}
	return true
}

func (s *Service) enqueueOCRBackfill(
	ctx context.Context,
	limit int,
	receivedOnly bool,
	respectScope bool,
	talkers []string,
) (scanned, enqueued int, err error) {
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		return 0, 0, errors.New("OCR index is not initialized")
	}
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10_000 {
		limit = 10_000
	}
	sessions, err := s.db.GetSessions("", 0, 0)
	if err != nil {
		return 0, 0, err
	}
	explicitTalkers := make(map[string]struct{}, len(talkers))
	accepted := 0
	waitingMedia := 0
	for _, talker := range talkers {
		talker = strings.TrimSpace(talker)
		if talker != "" {
			explicitTalkers[strings.ToLower(talker)] = struct{}{}
		}
	}
	for _, session := range sessions.Items {
		if ctx.Err() != nil || accepted >= limit {
			break
		}
		talker := strings.TrimSpace(session.UserName)
		if talker == "" {
			continue
		}
		if len(explicitTalkers) > 0 {
			if _, ok := explicitTalkers[strings.ToLower(talker)]; !ok {
				continue
			}
		}
		if respectScope && !runtime.config.AllowsTalker(talker) {
			continue
		}
		messages, queryErr := s.db.GetMessages(time.Time{}, time.Time{}, talker, "", "", 0, 0)
		if queryErr != nil {
			runtime.setError(queryErr)
			continue
		}
		for _, message := range messages {
			if ctx.Err() != nil || accepted >= limit {
				break
			}
			scanned++
			s.updateOCRBackfillProgress(scanned, enqueued)
			if message == nil || message.Type != model.MessageTypeImage || (receivedOnly && message.IsSelf) {
				continue
			}
			ref, ok := imageOCRRef(message)
			if !ok {
				continue
			}
			status := ocr.StatusWaitingMedia
			if ready, _ := s.ocrImageReady(ref); ready {
				status = ocr.StatusPending
			}
			_, inserted, enqueueErr := runtime.store.EnqueueWithStatus(ctx, ref, status)
			if enqueueErr != nil {
				runtime.setError(enqueueErr)
				continue
			}
			if inserted {
				accepted++
				if status == ocr.StatusPending {
					enqueued++
				} else {
					waitingMedia++
				}
				s.updateOCRBackfillProgress(scanned, enqueued)
			}
		}
	}
	if enqueued > 0 {
		runtime.mu.Lock()
		runtime.enqueued += uint64(enqueued)
		runtime.mu.Unlock()
		runtime.wakeWorker()
	}
	if waitingMedia > 0 {
		runtime.wakeMediaGate()
	}
	return scanned, enqueued, ctx.Err()
}
