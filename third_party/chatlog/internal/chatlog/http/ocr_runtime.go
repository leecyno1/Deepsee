package http

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechat/imgdownload"
)

type imageOCRRuntime struct {
	config conf.OCRConfig
	store  *ocr.Store
	client *ocr.Client

	ctx                 context.Context
	cancel              context.CancelFunc
	wake                chan struct{}
	scanWake            chan struct{}
	mediaWake           chan struct{}
	changeSubscriptions []func()
	wg                  sync.WaitGroup
	mediaWatcher        *fsnotify.Watcher
	mediaWatchMu        sync.Mutex
	mediaWatchDirs      map[string]struct{}
	mediaRetryMu        sync.Mutex

	mu             sync.RWMutex
	scanMu         sync.Mutex
	startedAt      int64
	lastScanAt     int64
	lastSuccessAt  int64
	lastErrorAt    int64
	lastError      string
	scanned        uint64
	enqueued       uint64
	processed      uint64
	failed         uint64
	initialized    bool
	scannerRunning bool
	workerRunning  bool

	lastScanDurationMS int64
	lastScanMessages   uint64
	lastScanEnqueued   uint64
	currentRecordID    int64
	currentTalker      string
	currentMediaKey    string
	currentStartedAt   int64
	currentStage       string
	currentDetail      string
	localServiceReady  bool

	// Auto original-image download (Frida) progress for status UI.
	autoDownloadActive     bool
	autoDownloadInFlight   bool // true from first "triggering" until ready/error/skipped
	autoDownloadStage      string
	autoDownloadDetail     string
	autoDownloadMediaKey   string
	autoDownloadTalker     string
	autoDownloadRecordID   int64
	autoDownloadStartedAt  int64
	autoDownloadFinishedAt int64
	autoDownloadLastError  string
	autoDownloadTriggered  uint64
	autoDownloadSucceeded  uint64
	autoDownloadFailed     uint64
	autoDownloadSkipped    uint64
}

func (s *Service) startOCRRuntime() error {
	s.ocrRuntimeMu.Lock()
	defer s.ocrRuntimeMu.Unlock()
	if s.ocrRuntime != nil {
		return nil
	}
	if _, ok := s.conf.(OCRConfig); !ok {
		return nil
	}
	config := s.configuredOCR()
	store, err := ocr.OpenStore(s.ocrIndexPath())
	if err != nil {
		return err
	}
	runtime := &imageOCRRuntime{
		config:      config,
		store:       store,
		wake:        make(chan struct{}, 1),
		scanWake:    make(chan struct{}, 1),
		mediaWake:   make(chan struct{}, 1),
		startedAt:   time.Now().Unix(),
		initialized: true,
	}
	s.ocrRuntime = runtime
	s.ocrLocalService = nil
	if config.Provider == conf.OCRProviderVLLM {
		manager := ocr.NewLocalServiceManager(config.Endpoint, ocr.LocalServiceOptions{
			AutoStart:   config.LocalAutoStartEnabled(),
			AutoRestart: config.LocalAutoRestartEnabled(),
		})
		if manager.Managed() {
			s.ocrLocalService = manager
			if config.Enabled || config.BackfillEnabled {
				manager.EnsureAsync()
			}
		}
	}
	if !config.Enabled && !config.BackfillEnabled {
		return nil
	}
	client, err := ocr.NewClient(config)
	if err != nil {
		runtime.setError(err)
		return nil
	}
	runtime.client = client
	runtime.ctx, runtime.cancel = context.WithCancel(context.Background())
	s.startOCRMediaWatcher(runtime)
	// Arm the resident image hook before the realtime scanner starts. Incoming
	// thumbnail requests are upgraded in WeChat's own receive thread, before any
	// OCR polling, so no client-side image open is needed.
	forceGlobalReceiveUpgrade := strings.TrimSpace(os.Getenv("CHATLOG_IMAGE_DOWNLOAD_TRIGGER")) == "1"
	globalReceiveUpgrade := ocrGlobalReceiveUpgradeRequested(config, forceGlobalReceiveUpgrade)
	if config.Enabled && config.AutoDownloadOriginal && imgdownload.FridaAvailable() {
		stage := "arming"
		detail := "正在提前挂载微信图片下载 Hook…"
		prewarm := imgdownload.PrewarmFridaDaemon
		if !globalReceiveUpgrade {
			stage = "scoped_arming"
			detail = "正在挂载监听范围图片下载 Hook…"
			prewarm = imgdownload.PrewarmScopedFridaDaemon
		}
		runtime.setAutoDownload(stage, detail, "", "", 0, false)
		prewarmCtx, prewarmCancel := context.WithTimeout(runtime.ctx, 8*time.Second)
		err := prewarm(prewarmCtx)
		if err == nil && !globalReceiveUpgrade {
			err = imgdownload.SetScopedDownloadTalkers(prewarmCtx, ocrScopedTalkers(config))
		}
		prewarmCancel()
		if err != nil {
			runtime.setAutoDownload("unavailable", "图片下载 Hook 预挂载失败："+err.Error(), "", "", 0, false)
			log.Warn().Err(err).Msg("prewarm image download hooks")
		} else if globalReceiveUpgrade {
			runtime.setAutoDownload("armed", "图片接收 Hook 已提前挂载，等待新图片", "", "", 0, false)
		} else {
			runtime.setAutoDownload("scoped_armed", "监听范围 Hook 已挂载，仅升级名单内图片", "", "", 0, false)
		}
	}
	if config.Enabled {
		subscriptionReady := false
		if feed, ok := s.db.(ports.MessageChangeFeed); ok {
			cancel, subscribeErr := feed.SubscribeMessageChanges(
				"ocr", time.Unix(runtime.startedAt, 0), s.consumeOCRMessageChangeBatch,
			)
			if subscribeErr != nil {
				runtime.setError(fmt.Errorf("subscribe OCR shared message changes: %w", subscribeErr))
			} else {
				runtime.changeSubscriptions = append(runtime.changeSubscriptions, cancel)
				subscriptionReady = true
			}
		}
		if !subscriptionReady {
			subscriber, ok := s.db.(DatabaseChangeSubscriber)
			if !ok {
				runtime.setError(fmt.Errorf("database change subscription is unavailable"))
			} else {
				subscriptionReady = true
				for _, group := range []string{"message", "session"} {
					cancel, subscribeErr := subscriber.SubscribeCallback(group, func(event fsnotify.Event) error {
						if event.Op.Has(fsnotify.Create) || event.Op.Has(fsnotify.Write) ||
							event.Op.Has(fsnotify.Rename) || event.Op.Has(fsnotify.Remove) {
							select {
							case runtime.scanWake <- struct{}{}:
							default:
							}
						}
						return nil
					})
					if subscribeErr != nil {
						for _, unsubscribe := range runtime.changeSubscriptions {
							unsubscribe()
						}
						runtime.changeSubscriptions = nil
						runtime.setError(fmt.Errorf("subscribe OCR database group %s: %w", group, subscribeErr))
						subscriptionReady = false
						break
					}
					runtime.changeSubscriptions = append(runtime.changeSubscriptions, cancel)
				}
			}
		}
		if subscriptionReady {
			runtime.scannerRunning = true
			if provider, shared := s.db.(MessageChangeStatusProvider); shared && provider.MessageChangeStats().Running {
				// One startup catch-up closes the gap since the persisted OCR cursor;
				// live changes are consumed from the shared single-read stream.
				s.scanNewImages(runtime)
			} else {
				runtime.wg.Add(1)
				go s.runOCRScanner(runtime)
			}
		}
	}
	// Classify persisted unfinished records before the OCR worker starts. This
	// prevents a thumbnail left from an earlier run from racing into recognition.
	if _, _, err := s.refreshOCRMediaGate(runtime); err != nil {
		runtime.setError(err)
	}
	if config.Enabled && config.AutoDownloadOriginal && !globalReceiveUpgrade && imgdownload.FridaAvailable() {
		if _, err := s.restoreOCRWaitingMediaAllowlist(runtime); err != nil {
			runtime.setError(fmt.Errorf("restore waiting OCR media retries: %w", err))
		}
	}
	runtime.wg.Add(1)
	go s.runOCRMediaGate(runtime)
	runtime.wakeMediaGate()
	runtime.workerRunning = true
	runtime.wg.Add(1)
	go s.runOCRWorker(runtime)
	log.Info().
		Str("provider", config.Provider).
		Str("model", config.Model).
		Str("index", store.Path()).
		Bool("realtime", config.Enabled).
		Bool("backfill", config.BackfillEnabled).
		Msg("image OCR pipeline started")
	return nil
}

func (s *Service) stopOCRRuntime() {
	s.cancelOCRBackfill()
	s.ocrRuntimeMu.Lock()
	runtime := s.ocrRuntime
	s.ocrRuntime = nil
	s.ocrRuntimeMu.Unlock()
	if runtime == nil {
		return
	}
	for _, unsubscribe := range runtime.changeSubscriptions {
		if unsubscribe != nil {
			unsubscribe()
		}
	}
	runtime.changeSubscriptions = nil
	if runtime.cancel != nil {
		runtime.cancel()
	}
	runtime.wg.Wait()
	s.stopOCRMediaWatcher(runtime)
	if runtime.client != nil {
		runtime.client.Close()
	}
	if runtime.store != nil {
		if err := runtime.store.Close(); err != nil {
			log.Debug().Err(err).Msg("close image OCR index")
		}
	}
	// Release resident Frida attach so WeChat is not left hooked after OCR stops.
	imgdownload.StopFridaDaemon()
}

func (s *Service) currentOCRRuntime() *imageOCRRuntime {
	s.ocrRuntimeMu.RLock()
	defer s.ocrRuntimeMu.RUnlock()
	return s.ocrRuntime
}

func (s *Service) currentOCRLocalService() *ocr.LocalServiceManager {
	s.ocrRuntimeMu.RLock()
	defer s.ocrRuntimeMu.RUnlock()
	return s.ocrLocalService
}

func (runtime *imageOCRRuntime) setError(err error) {
	if runtime == nil || err == nil {
		return
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 1000 {
		message = message[:1000]
	}
	runtime.mu.Lock()
	runtime.lastError = message
	runtime.lastErrorAt = time.Now().Unix()
	runtime.mu.Unlock()
}

func (runtime *imageOCRRuntime) wakeWorker() {
	if runtime == nil {
		return
	}
	select {
	case runtime.wake <- struct{}{}:
	default:
	}
}

func (s *Service) runOCRScanner(runtime *imageOCRRuntime) {
	defer runtime.wg.Done()
	// Initialize cursors immediately. With backfill disabled, this establishes
	// an activation watermark before any OCR request is made.
	s.scanNewImages(runtime)
	for {
		select {
		case <-runtime.ctx.Done():
			return
		case <-runtime.scanWake:
			debounce := 250 * time.Millisecond
			if runtime.config.AutoDownloadOriginal && !runtime.config.ScopeAll() {
				// The hook already knows the prearmed talker scope; keep metadata
				// classification responsive while still allowing the WAL to settle.
				debounce = 40 * time.Millisecond
			}
			timer := time.NewTimer(debounce)
			select {
			case <-runtime.ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}
			for {
				select {
				case <-runtime.scanWake:
				default:
					goto scan
				}
			}
		scan:
			s.scanNewImages(runtime)
		}
	}
}

func (s *Service) scanNewImages(runtime *imageOCRRuntime) {
	if runtime == nil || runtime.store == nil || !s.db.Ready() {
		return
	}
	runtime.scanMu.Lock()
	defer runtime.scanMu.Unlock()
	started := time.Now()
	var batchScanned uint64
	var batchEnqueued uint64
	sessions, err := s.db.GetSessions("", 0, 0)
	if err != nil {
		runtime.setError(fmt.Errorf("scan OCR sessions: %w", err))
		return
	}
	for _, session := range sessions.Items {
		if runtime.ctx.Err() != nil {
			return
		}
		talker := strings.TrimSpace(session.UserName)
		if talker == "" {
			continue
		}
		if !runtime.config.AllowsTalker(talker) {
			continue
		}
		messageTime, localID, found, err := runtime.store.Cursor(runtime.ctx, talker)
		if err != nil {
			runtime.setError(err)
			continue
		}
		if !found && !runtime.config.BackfillOnStart {
			// Use the activation time, not the session table watermark. Some
			// folded/official sessions expose a stale last-message timestamp;
			// using it would enqueue historical images on first enable.
			cursor := model.MessageCursor{Timestamp: runtime.startedAt, LocalID: int64(^uint64(0) >> 1)}
			if err := runtime.store.SetCursor(runtime.ctx, talker, cursor.Timestamp, cursor.LocalID); err != nil {
				runtime.setError(err)
			}
			messageTime = cursor.Timestamp
			localID = cursor.LocalID
		}
		cursor := model.MessageCursor{Timestamp: messageTime, LocalID: localID}
		for {
			messages, err := s.db.GetMessagesAfter(talker, cursor, 200)
			if err != nil {
				runtime.setError(fmt.Errorf("scan OCR messages for %s: %w", talker, err))
				break
			}
			if len(messages) == 0 {
				break
			}
			progressed := false
			pageCursor := cursor
			pageRefs := make([]ocr.ImageRef, 0, len(messages))
			for _, message := range messages {
				if message == nil {
					continue
				}
				next := model.MessageCursor{Timestamp: message.Time.Unix(), LocalID: message.DBLocalID}
				if !pageCursor.Before(next) {
					continue
				}
				progressed = true
				runtime.mu.Lock()
				runtime.scanned++
				runtime.mu.Unlock()
				batchScanned++
				if (runtime.config.BackfillOnStart || message.Time.Unix() >= runtime.startedAt) &&
					message.Type == model.MessageTypeImage &&
					(!runtime.config.ReceivedOnly || !message.IsSelf) {
					ref, ok := imageOCRRef(message)
					if ok {
						pageRefs = append(pageRefs, ref)
					}
				}
				pageCursor = next
			}
			if progressed {
				if runtime.config.AutoDownloadOriginal && !runtime.config.ScopeAll() && len(pageRefs) > 0 {
					registerCtx, cancel := context.WithTimeout(runtime.ctx, 1200*time.Millisecond)
					registerErr := imgdownload.AllowScopedDownloadFileIDs(
						registerCtx, scopedOCRFileIDs(pageRefs), 2*time.Minute,
					)
					cancel()
					if registerErr != nil {
						runtime.setError(fmt.Errorf("register scoped OCR image downloads: %w", registerErr))
					}
				}
				pendingRefs, waitingRefs := s.classifyOCRRefs(pageRefs)
				applied, applyErr := runtime.store.ApplyScanBatchClassified(
					runtime.ctx, talker, pendingRefs, waitingRefs, pageCursor.Timestamp, pageCursor.LocalID,
				)
				if applyErr != nil {
					runtime.setError(applyErr)
					break
				}
				cursor = pageCursor
				if applied.WaitingMedia > 0 {
					runtime.wakeMediaGate()
				}
				if applied.Pending > 0 {
					runtime.mu.Lock()
					runtime.enqueued += uint64(applied.Pending)
					runtime.mu.Unlock()
					batchEnqueued += uint64(applied.Pending)
					runtime.wakeWorker()
				}
			}
			if !progressed || len(messages) < 200 {
				break
			}
		}
	}
	runtime.mu.Lock()
	runtime.lastScanAt = time.Now().Unix()
	runtime.lastScanDurationMS = time.Since(started).Milliseconds()
	runtime.lastScanMessages = batchScanned
	runtime.lastScanEnqueued = batchEnqueued
	runtime.mu.Unlock()
}

func (s *Service) runOCRWorker(runtime *imageOCRRuntime) {
	defer runtime.wg.Done()
	for {
		next, exists, err := runtime.store.NextWorkAt(runtime.ctx)
		if err != nil {
			runtime.setError(err)
		}
		if !exists || err != nil {
			select {
			case <-runtime.ctx.Done():
				return
			case <-runtime.wake:
			}
			continue
		}
		if delay := time.Until(next); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-runtime.ctx.Done():
				stopAndDrainTimer(timer)
				return
			case <-runtime.wake:
				stopAndDrainTimer(timer)
			case <-timer.C:
			}
			continue
		}
		if !s.ocrLocalServiceReady(runtime) {
			if manager := s.currentOCRLocalService(); manager != nil && !manager.WorkerProbeEnabled() {
				select {
				case <-runtime.ctx.Done():
					return
				case <-runtime.wake:
				}
				continue
			}
			// A due task exists and the managed model is still starting. Probe only
			// while work is waiting; an empty queue performs no health polling.
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-runtime.ctx.Done():
				stopAndDrainTimer(timer)
				return
			case <-runtime.wake:
				stopAndDrainTimer(timer)
			case <-timer.C:
			}
			continue
		}
		s.drainOCRQueue(runtime)
	}
}

func (s *Service) ocrLocalServiceReady(runtime *imageOCRRuntime) bool {
	manager := s.currentOCRLocalService()
	if manager == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(runtime.ctx, time.Second)
	ready := manager.Ready(ctx)
	cancel()
	if !ready {
		runtime.localServiceReady = false
		manager.EnsureAsync()
		return false
	}
	if runtime.localServiceReady {
		return true
	}
	runtime.localServiceReady = true
	retried, err := runtime.store.RetryConnectionFailures(runtime.ctx)
	if err != nil {
		runtime.setError(err)
	} else if retried > 0 {
		runtime.wakeWorker()
	}
	runtime.mu.Lock()
	if strings.Contains(strings.ToLower(runtime.lastError), "connection refused") {
		runtime.lastError = ""
		runtime.lastErrorAt = 0
	}
	runtime.mu.Unlock()
	return true
}

func (s *Service) drainOCRQueue(runtime *imageOCRRuntime) {
	for runtime.ctx.Err() == nil {
		record, err := runtime.store.NextPending(runtime.ctx)
		if err != nil {
			runtime.setError(err)
			return
		}
		if record == nil {
			return
		}
		if !s.ensureOCRRecordMediaReady(runtime, record) {
			continue
		}
		if err := runtime.store.MarkProcessing(runtime.ctx, record.ID); err != nil {
			runtime.setError(err)
			return
		}
		runtime.setCurrentTask(record.ID, record.Talker, record.MediaKey, "loading", "定位本地图片文件")
		result, err := s.recognizeOCRAfterImageSettles(runtime, record)
		if err == nil {
			runtime.setCurrentTask(record.ID, record.Talker, record.MediaKey, "saving", "写入识别结果与索引")
			err = runtime.store.MarkSucceeded(
				runtime.ctx,
				record.ID,
				result.ContentHash,
				result.Provider,
				result.Model,
				result.Description,
				result.Text,
				result.Markdown,
				result.Layout,
			)
		}
		if err != nil {
			if runtime.ctx.Err() != nil {
				return
			}
			_ = runtime.store.MarkFailed(runtime.ctx, record.ID, err)
			runtime.setError(err)
			runtime.mu.Lock()
			runtime.failed++
			runtime.clearCurrentTaskLocked()
			runtime.mu.Unlock()
			continue
		}
		completed := *record
		completed.ContentHash = result.ContentHash
		completed.Provider = result.Provider
		completed.Model = result.Model
		completed.Description = result.Description
		completed.OCRText = result.Text
		completed.Markdown = result.Markdown
		completed.Layout = append([]byte(nil), result.Layout...)
		completed.Status = ocr.StatusSucceeded
		if notifier, ok := s.db.(interface {
			NotifyMessageHookOCR(*model.Message, string) (bool, error)
		}); ok {
			ocrText := strings.TrimSpace(strings.Join([]string{result.Description, result.Text}, "\n"))
			if _, notifyErr := notifier.NotifyMessageHookOCR(ocrRecordMessage(completed), ocrText); notifyErr != nil {
				log.Warn().Err(notifyErr).Int64("ocr_record_id", record.ID).Msg("notify OCR keyword hook")
			}
		}
		runtime.mu.Lock()
		runtime.processed++
		runtime.lastSuccessAt = time.Now().Unix()
		runtime.lastError = ""
		runtime.clearCurrentTaskLocked()
		runtime.mu.Unlock()
	}
}

func (runtime *imageOCRRuntime) setCurrentTask(recordID int64, talker, mediaKey, stage, detail string) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.currentRecordID = recordID
	runtime.currentTalker = talker
	runtime.currentMediaKey = mediaKey
	if runtime.currentStartedAt == 0 {
		runtime.currentStartedAt = time.Now().Unix()
	}
	runtime.currentStage = stage
	runtime.currentDetail = detail
}

func (runtime *imageOCRRuntime) clearCurrentTaskLocked() {
	runtime.currentRecordID = 0
	runtime.currentTalker = ""
	runtime.currentMediaKey = ""
	runtime.currentStartedAt = 0
	runtime.currentStage = ""
	runtime.currentDetail = ""
	// Keep last auto-download stage/detail for status UI; only clear "in progress".
	if runtime.autoDownloadActive {
		runtime.autoDownloadActive = false
		if runtime.autoDownloadFinishedAt == 0 {
			runtime.autoDownloadFinishedAt = time.Now().Unix()
		}
	}
}

func (runtime *imageOCRRuntime) setAutoDownload(stage, detail, mediaKey, talker string, recordID int64, active bool) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.setAutoDownloadLocked(stage, detail, mediaKey, talker, recordID, active)
}

func (runtime *imageOCRRuntime) setAutoDownloadLocked(stage, detail, mediaKey, talker string, recordID int64, active bool) {
	now := time.Now().Unix()
	prevActive := runtime.autoDownloadActive
	prevStage := runtime.autoDownloadStage
	prevInFlight := runtime.autoDownloadInFlight

	// "checking" must not cancel an in-flight attempt: EnsureOriginal always emits
	// checking right after we mark triggering, and that used to clear active so
	// a subsequent ready never incremented succeeded (stayed at 0).
	if stage == "checking" && (prevInFlight || prevActive) {
		active = true
	}

	runtime.autoDownloadActive = active
	runtime.autoDownloadStage = stage
	runtime.autoDownloadDetail = detail
	runtime.autoDownloadMediaKey = mediaKey
	runtime.autoDownloadTalker = talker
	if recordID > 0 {
		runtime.autoDownloadRecordID = recordID
	}
	if active && (!prevActive || runtime.autoDownloadStartedAt == 0) {
		runtime.autoDownloadStartedAt = now
		runtime.autoDownloadFinishedAt = 0
	}
	if !active && prevActive {
		runtime.autoDownloadFinishedAt = now
	}

	// Count by attempt lifecycle (inFlight), not by fleeting active flags.
	// One Frida ensure cycle: triggering → (+checking/waiting)* → ready|error|skipped.
	switch stage {
	case "triggering":
		if !runtime.autoDownloadInFlight {
			runtime.autoDownloadInFlight = true
			runtime.autoDownloadTriggered++
		}
		runtime.autoDownloadLastError = ""
	case "waiting", "checking":
		// Keep attempt open; no counter changes.
		if runtime.autoDownloadInFlight {
			runtime.autoDownloadLastError = ""
		}
	case "ready":
		// Success only for an open ensure/download attempt (not pure "already local").
		if runtime.autoDownloadInFlight {
			runtime.autoDownloadSucceeded++
			runtime.autoDownloadInFlight = false
		}
		runtime.autoDownloadLastError = ""
		runtime.autoDownloadActive = false
		if prevActive || prevInFlight {
			runtime.autoDownloadFinishedAt = now
		}
	case "local":
		// Local mid/HD already present; not a download success. Do not touch counters.
		runtime.autoDownloadInFlight = false
		runtime.autoDownloadActive = false
		runtime.autoDownloadLastError = ""
		if prevActive {
			runtime.autoDownloadFinishedAt = now
		}
	case "error":
		if runtime.autoDownloadInFlight {
			runtime.autoDownloadFailed++
			runtime.autoDownloadInFlight = false
		} else if prevActive && prevStage != "error" {
			// Stale reclaim / interrupt without a counted attempt.
			runtime.autoDownloadFailed++
		}
		runtime.autoDownloadLastError = detail
		runtime.autoDownloadActive = false
		if prevActive || prevInFlight {
			runtime.autoDownloadFinishedAt = now
		}
	case "skipped":
		if prevStage != "skipped" {
			runtime.autoDownloadSkipped++
		}
		runtime.autoDownloadInFlight = false
		runtime.autoDownloadActive = false
		if prevActive {
			runtime.autoDownloadFinishedAt = now
		}
	}

	// Mirror into current task detail when this download is for the active record.
	if runtime.currentRecordID != 0 && (recordID == 0 || recordID == runtime.currentRecordID) {
		switch stage {
		case "triggering", "waiting", "checking", "error", "ready", "local":
			if stage != "local" || prevInFlight || prevActive {
				runtime.currentStage = "download"
				runtime.currentDetail = detail
			}
		}
		if mediaKey != "" {
			runtime.currentMediaKey = mediaKey
		}
		if talker != "" {
			runtime.currentTalker = talker
		}
	}
}
