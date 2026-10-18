package http

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/wechat/imgdownload"
)

const (
	ocrWaitingMediaDetail    = "仅检测到缩略图，等待中图/原图落盘；未进入 OCR 识别队列"
	ocrMediaRetryMaxAttempts = 8
	ocrMediaRetryMinAllowTTL = 10 * time.Minute
	ocrMediaRetryAllowGrace  = 2 * time.Minute
	ocrMediaRetryBatchLimit  = 500
	ocrMediaReplayBatchLimit = 16
)

type ocrMediaRetryResult struct {
	Requested int     `json:"requested"`
	Armed     int     `json:"armed"`
	Triggered int     `json:"triggered"`
	Promoted  int     `json:"promoted"`
	Skipped   int     `json:"skipped"`
	Failed    int     `json:"failed"`
	IDs       []int64 `json:"ids,omitempty"`
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (runtime *imageOCRRuntime) wakeMediaGate() {
	if runtime == nil || runtime.mediaWake == nil {
		return
	}
	select {
	case runtime.mediaWake <- struct{}{}:
	default:
	}
}

// classifyOCRRefs applies the strict quality boundary before records are made
// visible to OCR workers. Thumbnail-only records are persisted as media
// waiters, not pending OCR tasks.
func (s *Service) classifyOCRRefs(refs []ocr.ImageRef) (pending, waiting []ocr.ImageRef) {
	pending = make([]ocr.ImageRef, 0, len(refs))
	waiting = make([]ocr.ImageRef, 0, len(refs))
	for _, ref := range refs {
		if ready, _ := s.ocrImageReady(ref); ready {
			pending = append(pending, ref)
		} else {
			waiting = append(waiting, ref)
		}
	}
	return pending, waiting
}

// ocrImageReady checks only local paths and quality markers; it never reads or
// decodes image bytes and never submits an OCR request.
func (s *Service) ocrImageReady(ref ocr.ImageRef) (bool, string) {
	path := s.resolveOCRImagePath(ref)
	dataDir := ""
	if s.conf != nil {
		dataDir = strings.TrimSpace(s.conf.GetDataDir())
	}
	if dataDir != "" {
		if best, ok := imgdownload.ScanBest(dataDir, ref.MediaKey, ref.MediaPath); ok {
			if path == "" || best.Tier > imgdownload.TierOfName(path) {
				path = best.Path
			}
		}
	}
	if !ocrImagePathUsable(path) {
		return false, path
	}
	return true, path
}

// refreshOCRMediaGate demotes thumbnail-only unfinished rows and promotes only
// those waiters for which a mid/big/HD (or ordinary local image) now exists.
func (s *Service) refreshOCRMediaGate(runtime *imageOCRRuntime) (waiting, promoted int, err error) {
	if runtime == nil || runtime.store == nil || runtime.ctx == nil {
		return 0, 0, nil
	}
	records, err := runtime.store.MediaGateRecords(runtime.ctx, 1000)
	if err != nil {
		return 0, 0, fmt.Errorf("list OCR media gate: %w", err)
	}
	watchDirs := make(map[string]struct{})
	for i := range records {
		record := &records[i]
		ready, path := s.ocrImageReady(record.ImageRef)
		if ready {
			if record.Status == ocr.StatusWaitingMedia {
				changed, markErr := runtime.store.MarkMediaReady(runtime.ctx, record.ID)
				if markErr != nil {
					return waiting, promoted, markErr
				}
				if changed {
					promoted++
					runtime.setAutoDownload(
						"ready",
						fmt.Sprintf("等待中的图片已获得中图/原图（记录 #%d）", record.ID),
						record.MediaKey, record.Talker, record.ID, false,
					)
				}
			}
			continue
		}
		waiting++
		if dir := s.ocrMediaWatchDir(record.ImageRef, path); dir != "" {
			watchDirs[dir] = struct{}{}
		}
		if record.Status != ocr.StatusWaitingMedia {
			if _, markErr := runtime.store.MarkWaitingMedia(runtime.ctx, record.ID, ocrWaitingMediaDetail); markErr != nil {
				return waiting, promoted, markErr
			}
		}
	}
	if watchErr := s.syncOCRMediaWatchDirs(runtime, watchDirs); watchErr != nil {
		return waiting, promoted, watchErr
	}
	return waiting, promoted, nil
}

func ocrMediaRetryDelay(attempt int) time.Duration {
	switch {
	case attempt <= 2:
		return 8 * time.Minute
	case attempt == 3:
		return 15 * time.Minute
	case attempt == 4:
		return 30 * time.Minute
	case attempt == 5:
		return time.Hour
	case attempt == 6:
		return 2 * time.Hour
	default:
		return 6 * time.Hour
	}
}

func ocrMediaRetryAllowTTL(record ocr.Record, now time.Time) time.Duration {
	ttl := ocrMediaRetryDelay(record.MediaRetryAttempts+1) + ocrMediaRetryAllowGrace
	if record.MediaNextRetryAt > now.Unix() {
		ttl = time.Unix(record.MediaNextRetryAt, 0).Sub(now) + ocrMediaRetryAllowGrace
	}
	if ttl < ocrMediaRetryMinAllowTTL {
		return ocrMediaRetryMinAllowTTL
	}
	return ttl
}

// restoreOCRWaitingMediaAllowlist restores the in-memory daemon allowlist
// after a Chatlog or Frida-daemon restart without consuming a retry attempt.
func (s *Service) restoreOCRWaitingMediaAllowlist(runtime *imageOCRRuntime) (int, error) {
	if runtime == nil || runtime.store == nil || runtime.ctx == nil {
		return 0, nil
	}
	records, err := runtime.store.WaitingMediaRecords(runtime.ctx, ocrMediaRetryBatchLimit)
	if err != nil {
		return 0, err
	}
	fileIDs := make([]string, 0, len(records)*3)
	allowTTL := ocrMediaRetryMinAllowTTL
	now := time.Now()
	armed := 0
	for _, record := range records {
		ids := scopedOCRFileIDs([]ocr.ImageRef{record.ImageRef})
		if len(ids) == 0 {
			continue
		}
		fileIDs = append(fileIDs, ids...)
		armed++
		if ttl := ocrMediaRetryAllowTTL(record, now); ttl > allowTTL {
			allowTTL = ttl
		}
	}
	if len(fileIDs) == 0 {
		return 0, nil
	}
	registerCtx, cancel := context.WithTimeout(runtime.ctx, 2*time.Second)
	err = imgdownload.AllowScopedDownloadFileIDs(registerCtx, fileIDs, allowTTL)
	cancel()
	if err != nil {
		return 0, err
	}
	runtime.setAutoDownload(
		"retry_armed",
		fmt.Sprintf("服务启动已恢复 %d 张等待中图的下载预授权", armed),
		"", "", 0, false,
	)
	return armed, nil
}

// retryOCRWaitingMedia refreshes the receive-hook allowlist and submits a
// bounded native StartC2CDownload replay for old thumbnail-only records. The
// replay request is rebuilt from persisted message/CDN metadata and never
// reuses pointers captured from an earlier WeChat request.
func (s *Service) retryOCRWaitingMedia(runtime *imageOCRRuntime, records []ocr.Record, manual bool) (ocrMediaRetryResult, error) {
	result := ocrMediaRetryResult{Requested: len(records), IDs: make([]int64, 0, len(records))}
	if runtime == nil || runtime.store == nil || runtime.ctx == nil || len(records) == 0 {
		return result, nil
	}
	runtime.mediaRetryMu.Lock()
	defer runtime.mediaRetryMu.Unlock()

	eligible := make([]ocr.Record, 0, len(records))
	fileIDs := make([]string, 0, len(records)*3)
	allowTTL := ocrMediaRetryMinAllowTTL
	for _, record := range records {
		if record.Status != ocr.StatusWaitingMedia {
			result.Skipped++
			continue
		}
		if ready, _ := s.ocrImageReady(record.ImageRef); ready {
			changed, err := runtime.store.MarkMediaReady(runtime.ctx, record.ID)
			if err != nil {
				return result, err
			}
			if changed {
				result.Promoted++
				result.IDs = append(result.IDs, record.ID)
			}
			continue
		}
		ids := scopedOCRFileIDs([]ocr.ImageRef{record.ImageRef})
		if len(ids) == 0 {
			next := time.Now().Add(ocrMediaRetryDelay(record.MediaRetryAttempts + 1)).Unix()
			_, err := runtime.store.MarkMediaRetry(runtime.ctx, record.ID, next, "缺少可用于重试的 CDN 文件标识")
			if err != nil {
				return result, err
			}
			result.Failed++
			continue
		}
		eligible = append(eligible, record)
		fileIDs = append(fileIDs, ids...)
		ttl := ocrMediaRetryDelay(record.MediaRetryAttempts+1) + ocrMediaRetryAllowGrace
		if ttl < ocrMediaRetryMinAllowTTL {
			ttl = ocrMediaRetryMinAllowTTL
		}
		if ttl > allowTTL {
			allowTTL = ttl
		}
	}
	if result.Promoted > 0 {
		runtime.mu.Lock()
		runtime.enqueued += uint64(result.Promoted)
		runtime.mu.Unlock()
		runtime.wakeWorker()
	}
	if len(eligible) == 0 {
		return result, nil
	}

	registerCtx, cancel := context.WithTimeout(runtime.ctx, 2*time.Second)
	registerErr := imgdownload.AllowScopedDownloadFileIDs(registerCtx, fileIDs, allowTTL)
	cancel()

	dataDir := ""
	if s.conf != nil {
		dataDir = strings.TrimSpace(s.conf.GetDataDir())
	}
	replayDetails := make(map[int64]string, len(eligible))
	replayedMedia := make(map[string]struct{}, len(eligible))
	// Production only upgrades a real WeChat request in-place. Synthetic
	// private-function replay omits parts of the media bookkeeping lifecycle and
	// remains behind an explicit research switch.
	if registerErr == nil && imgdownload.ActiveReplayEnabled() {
		trigger := imgdownload.NewScopedFridaTrigger("")
		replayed := 0
		for _, record := range eligible {
			identity := strings.TrimSpace(record.MediaPath) + "\x00" + strings.TrimSpace(record.CdnBigImgURL) + "\x00" + strings.TrimSpace(record.CdnMidImgURL)
			if _, exists := replayedMedia[identity]; exists {
				replayDetails[record.ID] = "同批次相同媒体已提交原生下载请求"
				continue
			}
			if replayed >= ocrMediaReplayBatchLimit {
				replayDetails[record.ID] = "下载预授权已刷新；等待下一轮原生下载提交"
				continue
			}
			replayedMedia[identity] = struct{}{}
			replayed++
			if replayed == 1 {
				runtime.setAutoDownload("triggering", "正在向微信提交等待中图的原生下载请求", record.MediaKey, record.Talker, record.ID, true)
			}
			replayCtx, replayCancel := context.WithTimeout(runtime.ctx, 10*time.Second)
			replayErr := trigger.RequestDownload(replayCtx, imgdownload.Request{
				DataDir:     dataDir,
				MediaKey:    record.MediaKey,
				MediaPath:   record.MediaPath,
				Talker:      record.Talker,
				MessageTime: record.MessageTime,
				DBLocalID:   record.DBLocalID,
				CDN: imgdownload.CDNFields{
					AesKey:       record.AesKey,
					CdnMidImgURL: record.CdnMidImgURL,
					CdnBigImgURL: record.CdnBigImgURL,
					CdnThumbURL:  record.CdnThumbURL,
					HdLength:     record.HdLength,
					Length:       record.Length,
				},
			})
			replayCancel()
			if replayErr != nil {
				replayDetails[record.ID] = "原生下载提交失败：" + replayErr.Error()
				result.Failed++
				continue
			}
			replayDetails[record.ID] = "已向微信提交原生中图下载请求；等待文件落盘"
			result.Triggered++
		}
		if replayed > 0 {
			detail := fmt.Sprintf("已提交 %d 张等待中图的原生下载请求", result.Triggered)
			if result.Failed > 0 {
				detail += fmt.Sprintf("，%d 张提交失败", result.Failed)
			}
			if result.Triggered > 0 {
				runtime.setAutoDownload("waiting", detail+"；等待文件落盘", "", "", 0, true)
			} else {
				runtime.setAutoDownload("error", detail, "", "", 0, false)
			}
		}
	}
	now := time.Now()
	for _, record := range eligible {
		attempt := record.MediaRetryAttempts + 1
		next := now.Add(ocrMediaRetryDelay(attempt)).Unix()
		detail := replayDetails[record.ID]
		if detail == "" {
			detail = "已刷新会话范围与文件标识预授权；等待微信产生原生图片请求"
			if !manual {
				detail = fmt.Sprintf("第 %d 次自动预授权已刷新；等待微信产生原生图片请求", attempt)
			}
		}
		if registerErr != nil {
			detail = "图片下载重试布防失败：" + registerErr.Error()
		}
		changed, err := runtime.store.MarkMediaRetry(runtime.ctx, record.ID, next, detail)
		if err != nil {
			return result, err
		}
		if !changed {
			result.Skipped++
			continue
		}
		if registerErr != nil {
			result.Failed++
			continue
		}
		result.Armed++
		result.IDs = append(result.IDs, record.ID)
	}
	if registerErr != nil {
		return result, registerErr
	}
	if result.Armed > 0 && result.Triggered == 0 {
		mode := "自动"
		if manual {
			mode = "手动"
		}
		runtime.setAutoDownload(
			"retry_armed",
			fmt.Sprintf("%s操作已为 %d 张等待中图刷新下载预授权", mode, result.Armed),
			"", "", 0, false,
		)
	}
	return result, nil
}

func (s *Service) autoRetryOCRWaitingMedia(runtime *imageOCRRuntime) (ocrMediaRetryResult, error) {
	if runtime == nil || runtime.store == nil || !runtime.config.AutoDownloadOriginal {
		return ocrMediaRetryResult{}, nil
	}
	records, err := runtime.store.DueMediaRetryRecords(
		runtime.ctx, time.Now().Unix(), ocrMediaRetryMaxAttempts, ocrMediaRetryBatchLimit,
	)
	if err != nil {
		return ocrMediaRetryResult{}, err
	}
	return s.retryOCRWaitingMedia(runtime, records, false)
}

// runOCRMediaGate performs bounded filesystem checks only while media is
// expected to land. Between checks it sleeps until a file event, database
// event, explicit wake, or the next persisted allowlist retry deadline.
func (s *Service) runOCRMediaGate(runtime *imageOCRRuntime) {
	defer runtime.wg.Done()
	for {
		var retryTimer *time.Timer
		var retryTimerC <-chan time.Time
		if runtime.config.AutoDownloadOriginal {
			next, exists, err := runtime.store.NextMediaRetryAt(runtime.ctx, ocrMediaRetryMaxAttempts)
			if err != nil {
				runtime.setError(err)
			} else if exists {
				delay := time.Until(next)
				if delay < 0 {
					delay = 0
				}
				retryTimer = time.NewTimer(delay)
				retryTimerC = retryTimer.C
			}
		}
		select {
		case <-runtime.ctx.Done():
			stopAndDrainTimer(retryTimer)
			return
		case <-runtime.mediaWake:
			stopAndDrainTimer(retryTimer)
		case <-retryTimerC:
		}
		if _, err := s.autoRetryOCRWaitingMedia(runtime); err != nil && runtime.ctx.Err() == nil {
			runtime.setError(fmt.Errorf("retry waiting OCR media: %w", err))
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			waiting, promoted, err := s.refreshOCRMediaGate(runtime)
			if err != nil {
				if runtime.ctx.Err() != nil {
					return
				}
				runtime.setError(err)
				break
			}
			if promoted > 0 {
				runtime.mu.Lock()
				runtime.enqueued += uint64(promoted)
				runtime.mu.Unlock()
				runtime.wakeWorker()
			}
			if waiting == 0 || time.Now().After(deadline) {
				break
			}
			delay := 250 * time.Millisecond
			if time.Until(deadline) < 25*time.Second {
				delay = time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-runtime.ctx.Done():
				stopAndDrainTimer(timer)
				return
			case <-runtime.mediaWake:
				stopAndDrainTimer(timer)
				deadline = time.Now().Add(30 * time.Second)
			case <-timer.C:
			}
		}
	}
}

func (s *Service) ensureOCRRecordMediaReady(runtime *imageOCRRuntime, record *ocr.Record) bool {
	if runtime == nil || record == nil {
		return false
	}
	ready, path := s.ocrImageReady(record.ImageRef)
	if ready {
		return true
	}
	detail := ocrWaitingMediaDetail
	if path != "" {
		detail += "（" + filepath.Base(path) + "）"
	}
	if _, err := runtime.store.MarkWaitingMedia(runtime.ctx, record.ID, detail); err != nil {
		runtime.setError(err)
		return false
	}
	runtime.setAutoDownload("waiting_media", detail, record.MediaKey, record.Talker, record.ID, false)
	runtime.wakeMediaGate()
	return false
}
