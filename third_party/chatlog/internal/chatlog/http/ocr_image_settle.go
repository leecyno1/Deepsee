package http

import (
	"errors"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/ocr"
)

const (
	ocrImageSettleAttempts = 12
	ocrImageSettleDelay    = 250 * time.Millisecond
)

// recognizeOCRAfterImageSettles closes the short race between WeChat creating
// the normal-image path and finishing the write. The OCR client validates the
// complete payload before making an HTTP request; an incomplete read is then
// reloaded here. Persistent corruption still falls through to the store's
// normal delayed retry schedule.
func (s *Service) recognizeOCRAfterImageSettles(runtime *imageOCRRuntime, record *ocr.Record) (ocr.Result, error) {
	for attempt := 0; ; attempt++ {
		data, contentType, err := s.loadImageForOCR(runtime.ctx, record.ImageRef)
		if err == nil {
			runtime.setCurrentTask(record.ID, record.Talker, record.MediaKey, "recognizing", "调用 OCR 识别服务")
			result, recognizeErr := runtime.client.Recognize(runtime.ctx, data, contentType)
			if recognizeErr == nil {
				return result, nil
			}
			err = recognizeErr
		}
		if !errors.Is(err, ocr.ErrIncompleteImage) || attempt+1 >= ocrImageSettleAttempts {
			return ocr.Result{}, err
		}
		runtime.setCurrentTask(record.ID, record.Talker, record.MediaKey, "loading", "图片仍在落盘，等待完整后自动重试")
		timer := time.NewTimer(ocrImageSettleDelay)
		select {
		case <-runtime.ctx.Done():
			stopAndDrainTimer(timer)
			return ocr.Result{}, runtime.ctx.Err()
		case <-timer.C:
		}
	}
}
