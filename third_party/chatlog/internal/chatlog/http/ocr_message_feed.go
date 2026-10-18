package http

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechat/imgdownload"
)

// consumeOCRMessageChangeBatch advances the OCR cursor from the shared message
// page without reopening WeChat message shards. Only image metadata accepted by
// the configured scope proceeds to the media-quality gate.
func (s *Service) consumeOCRMessageChangeBatch(ctx context.Context, batch ports.MessageChangeBatch) error {
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil || runtime.ctx == nil || runtime.ctx.Err() != nil {
		return nil
	}
	if !runtime.config.Enabled || !runtime.config.AllowsTalker(batch.Talker) {
		return nil
	}
	started := time.Now()
	runtime.scanMu.Lock()
	defer runtime.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	messages := append([]*model.Message(nil), batch.Messages...)
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i] == nil || messages[j] == nil {
			return messages[i] != nil && messages[j] == nil
		}
		if messages[i].Time.Equal(messages[j].Time) {
			return messages[i].DBLocalID < messages[j].DBLocalID
		}
		return messages[i].Time.Before(messages[j].Time)
	})
	messageTime, localID, found, err := runtime.store.Cursor(runtime.ctx, batch.Talker)
	if err != nil {
		return err
	}
	if !found && !runtime.config.BackfillOnStart {
		cursor := model.MessageCursor{Timestamp: runtime.startedAt, LocalID: int64(^uint64(0) >> 1)}
		if err := runtime.store.SetCursor(runtime.ctx, batch.Talker, cursor.Timestamp, cursor.LocalID); err != nil {
			return err
		}
		messageTime, localID = cursor.Timestamp, cursor.LocalID
	}
	cursor := model.MessageCursor{Timestamp: messageTime, LocalID: localID}
	pageCursor := cursor
	refs := make([]ocr.ImageRef, 0, len(messages))
	var scanned uint64
	for _, message := range messages {
		if message == nil || !pageCursor.BeforeMessage(message) {
			continue
		}
		next := model.MessageCursor{Timestamp: message.Time.Unix(), LocalID: message.DBLocalID}
		scanned++
		if (runtime.config.BackfillOnStart || message.Time.Unix() >= runtime.startedAt) &&
			message.Type == model.MessageTypeImage &&
			(!runtime.config.ReceivedOnly || !message.IsSelf) {
			if ref, ok := imageOCRRef(message); ok {
				refs = append(refs, ref)
			}
		}
		pageCursor = next
	}
	if !cursor.Before(pageCursor) {
		return nil
	}
	if runtime.config.AutoDownloadOriginal && !runtime.config.ScopeAll() && len(refs) > 0 {
		registerCtx, cancel := context.WithTimeout(runtime.ctx, 1200*time.Millisecond)
		registerErr := imgdownload.AllowScopedDownloadFileIDs(registerCtx, scopedOCRFileIDs(refs), 2*time.Minute)
		cancel()
		if registerErr != nil {
			return fmt.Errorf("register shared-stream OCR downloads: %w", registerErr)
		}
	}
	pendingRefs, waitingRefs := s.classifyOCRRefs(refs)
	applied, err := runtime.store.ApplyScanBatchClassified(
		runtime.ctx, batch.Talker, pendingRefs, waitingRefs, pageCursor.Timestamp, pageCursor.LocalID,
	)
	if err != nil {
		return err
	}
	if applied.WaitingMedia > 0 {
		runtime.wakeMediaGate()
	}
	if applied.Pending > 0 {
		runtime.wakeWorker()
	}
	runtime.mu.Lock()
	runtime.scanned += scanned
	runtime.enqueued += uint64(applied.Pending)
	runtime.lastScanAt = time.Now().Unix()
	runtime.lastScanDurationMS = time.Since(started).Milliseconds()
	runtime.lastScanMessages = scanned
	runtime.lastScanEnqueued = uint64(applied.Pending)
	runtime.mu.Unlock()
	return nil
}
