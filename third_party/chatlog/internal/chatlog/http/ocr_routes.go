package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	chaterrors "github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechat/imgdownload"
	"github.com/sjzar/chatlog/pkg/util"
)

func (s *Service) initOCRRoutes(api *gin.RouterGroup) {
	api.GET("/ocr/search", s.handleOCRSearch)
	api.POST("/ocr/backfill", s.handleOCRBackfill)
	api.POST("/ocr/backfill/stop", s.handleOCRBackfillStop)
	api.GET("/ocr/index/:id", s.handleOCRRecordGet)
	api.DELETE("/ocr/index/succeeded", s.handleOCRSucceededClear)
	api.POST("/ocr/index/waiting-media/retry", s.handleOCRWaitingMediaRetryAll)
	api.POST("/ocr/index/:id/media-retry", s.handleOCRWaitingMediaRetry)
	api.POST("/ocr/index/:id/retry", s.handleOCRRetry)
	api.POST("/ocr/index/:id/rerecognize", s.handleOCRRerecognize)
	api.POST("/ocr/local-service/start", s.handleOCRLocalServiceStart)
	api.POST("/ocr/local-service/stop", s.handleOCRLocalServiceStop)
}

func (s *Service) handleOCRStatus(c *gin.Context) {
	writeJSON(c, s.ocrStatus(c.Request.Context()))
}

type ocrConfigRequest struct {
	Enabled              bool     `json:"enabled"`
	BackfillEnabled      bool     `json:"backfill_enabled"`
	AutoDownloadOriginal bool     `json:"auto_download_original"`
	LocalAutoStart       *bool    `json:"local_auto_start"`
	LocalAutoRestart     *bool    `json:"local_auto_restart"`
	Mode                 string   `json:"mode"`
	Provider             string   `json:"provider"`
	Endpoint             string   `json:"endpoint"`
	APIKey               *string  `json:"api_key"`
	ClearAPIKey          bool     `json:"clear_api_key"`
	Model                string   `json:"model"`
	PollIntervalSec      int      `json:"poll_interval_sec"`
	RequestTimeout       int      `json:"request_timeout_sec"`
	ReceivedOnly         bool     `json:"received_only"`
	ScopeAll             bool     `json:"scope_all"`
	ListenContacts       []string `json:"listen_contacts"`
	ListenChatRooms      []string `json:"listen_chatrooms"`
}

func ocrMode(provider string) string {
	if provider == conf.OCRProviderMaaS {
		return "api"
	}
	return "local"
}

func ocrEnvironmentOverrides() []string {
	names := []string{
		"CHATLOG_OCR_ENABLED",
		"CHATLOG_OCR_BACKFILL_ENABLED",
		"CHATLOG_OCR_AUTO_DOWNLOAD_ORIGINAL",
		"CHATLOG_IMAGE_DOWNLOAD_TRIGGER",
		"CHATLOG_OCR_PROVIDER",
		"CHATLOG_OCR_ENDPOINT",
		"CHATLOG_OCR_API_KEY",
		"CHATLOG_OCR_MODEL",
		"CHATLOG_OCR_POLL_INTERVAL_SEC",
		"CHATLOG_OCR_REQUEST_TIMEOUT_SEC",
		"CHATLOG_OCR_BACKFILL_ON_START",
		"CHATLOG_OCR_RECEIVED_ONLY",
		"CHATLOG_OCR_LISTEN_CONTACTS",
		"CHATLOG_OCR_LISTEN_CHATROOMS",
		"CHATLOG_OCR_LOCAL_AUTO_START",
		"CHATLOG_OCR_LOCAL_AUTO_RESTART",
		"CHATLOG_OCR_RUNTIME_DIR",
		"CHATLOG_OCR_LOCAL_START_SCRIPT",
		"CHATLOG_OCR_LOCAL_STOP_SCRIPT",
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := os.LookupEnv(name); ok {
			out = append(out, name)
		}
	}
	return out
}

func (s *Service) handleOCRConfigGet(c *gin.Context) {
	writeJSON(c, s.ocrConfigResponse(s.configuredOCR()))
}

func (s *Service) ocrConfigResponse(config conf.OCRConfig) gin.H {
	config = conf.NormalizeOCRConfig(config)
	return gin.H{
		"enabled":                 config.Enabled,
		"backfill_enabled":        config.BackfillEnabled,
		"auto_download_original":  config.AutoDownloadOriginal,
		"auto_download_available": s.ocrAutoDownloadAvailable(),
		"local_auto_start":        config.LocalAutoStartEnabled(),
		"local_auto_restart":      config.LocalAutoRestartEnabled(),
		"mode":                    ocrMode(config.Provider),
		"provider":                config.Provider,
		"endpoint":                config.Endpoint,
		"api_key_configured":      strings.TrimSpace(config.APIKey) != "",
		"model":                   config.Model,
		"trigger_mode":            "database_event",
		"poll_interval_sec":       config.PollIntervalSec,
		"request_timeout_sec":     config.RequestTimeout,
		"received_only":           config.ReceivedOnly,
		"scope_all":               config.ScopeAll(),
		"listen_contacts":         config.ListenContactsList(),
		"listen_chatrooms":        config.ListenChatRoomsList(),
		"environment_overrides":   ocrEnvironmentOverrides(),
	}
}

func ocrCallTargetChanged(previous, next conf.OCRConfig) bool {
	previous = conf.NormalizeOCRConfig(previous)
	next = conf.NormalizeOCRConfig(next)
	return previous.Provider != next.Provider ||
		previous.Endpoint != next.Endpoint ||
		previous.Model != next.Model ||
		previous.APIKey != next.APIKey
}

func (s *Service) handleOCRConfigSet(c *gin.Context) {
	var request ocrConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		chaterrors.Err(c, chaterrors.InvalidArg("body"))
		return
	}
	storage, ok := s.conf.(OCRConfigStorage)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "当前启动方式未提供 OCR 配置持久化能力"})
		return
	}
	previous := s.configuredOCR()
	next := storage.GetStoredOCRConfig()
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	switch mode {
	case "local":
		provider = conf.OCRProviderVLLM
	case "api":
		provider = conf.OCRProviderMaaS
	case "":
	default:
		chaterrors.Err(c, chaterrors.InvalidArg("mode"))
		return
	}
	if provider != conf.OCRProviderVLLM && provider != conf.OCRProviderMaaS {
		chaterrors.Err(c, chaterrors.InvalidArg("provider"))
		return
	}
	if request.PollIntervalSec < 1 || request.PollIntervalSec > 3600 {
		chaterrors.Err(c, chaterrors.InvalidArg("poll_interval_sec"))
		return
	}
	if request.RequestTimeout < 10 || request.RequestTimeout > 1800 {
		chaterrors.Err(c, chaterrors.InvalidArg("request_timeout_sec"))
		return
	}
	endpoint := strings.TrimSpace(request.Endpoint)
	if endpoint != "" {
		if err := validateAbsoluteHTTPURL(endpoint); err != nil {
			chaterrors.Err(c, chaterrors.InvalidArg("endpoint"))
			return
		}
	}
	next.Enabled = request.Enabled
	next.BackfillEnabled = request.BackfillEnabled
	next.AutoDownloadOriginal = request.AutoDownloadOriginal
	if request.LocalAutoStart != nil {
		next.LocalAutoStart = boolPointer(*request.LocalAutoStart)
	}
	if request.LocalAutoRestart != nil {
		next.LocalAutoRestart = boolPointer(*request.LocalAutoRestart)
	}
	next.Provider = provider
	next.Endpoint = endpoint
	next.Model = strings.TrimSpace(request.Model)
	next.PollIntervalSec = request.PollIntervalSec
	next.RequestTimeout = request.RequestTimeout
	next.ReceivedOnly = request.ReceivedOnly
	if request.ClearAPIKey {
		next.APIKey = ""
	} else if request.APIKey != nil && strings.TrimSpace(*request.APIKey) != "" {
		next.APIKey = strings.TrimSpace(*request.APIKey)
	}
	if request.ScopeAll {
		next.ListenContacts = ""
		next.ListenChatRooms = ""
	} else {
		next.ListenContacts = strings.Join(request.ListenContacts, ",")
		next.ListenChatRooms = strings.Join(request.ListenChatRooms, ",")
	}
	next = conf.NormalizeOCRConfig(next)
	effectiveKey := strings.TrimSpace(next.APIKey)
	if value, ok := os.LookupEnv("CHATLOG_OCR_API_KEY"); ok {
		effectiveKey = strings.TrimSpace(value)
	}
	if provider == conf.OCRProviderMaaS &&
		(next.Enabled || next.BackfillEnabled) &&
		effectiveKey == "" {
		chaterrors.Err(c, chaterrors.InvalidArg("api_key"))
		return
	}

	s.ocrReconfigureMu.Lock()
	defer s.ocrReconfigureMu.Unlock()
	if err := storage.UpdateOCRConfig(next); err != nil {
		chaterrors.Err(c, err)
		return
	}
	s.stopOCRRuntime()
	effectiveNext := s.configuredOCR()
	targetChanged := ocrCallTargetChanged(previous, effectiveNext)
	retrySync := gin.H{
		"changed":  targetChanged,
		"requeued": int64(0),
		"provider": effectiveNext.Provider,
		"model":    effectiveNext.Model,
	}
	if targetChanged {
		store, openErr := ocr.OpenStore(s.ocrIndexPath())
		if openErr != nil {
			retrySync["error"] = openErr.Error()
		} else {
			requeued, syncErr := store.SyncRetryableTarget(
				c.Request.Context(),
				effectiveNext.Provider,
				effectiveNext.Model,
			)
			closeErr := store.Close()
			if syncErr != nil {
				retrySync["error"] = syncErr.Error()
			} else if closeErr != nil {
				retrySync["error"] = closeErr.Error()
			} else {
				retrySync["requeued"] = requeued
			}
		}
	}
	if err := s.startOCRRuntime(); err != nil {
		chaterrors.Err(c, err)
		return
	}
	response := s.ocrConfigResponse(s.configuredOCR())
	response["retry_sync"] = retrySync
	writeJSON(c, response)
}

func boolPointer(value bool) *bool {
	return &value
}

func (s *Service) handleOCRLocalServiceStart(c *gin.Context) {
	manager := s.currentOCRLocalService()
	if manager == nil || !manager.Managed() {
		c.JSON(http.StatusConflict, gin.H{"error": "当前 OCR 配置未使用本机托管服务"})
		return
	}
	if err := manager.Start(); err != nil {
		chaterrors.Err(c, err)
		return
	}
	if runtime := s.currentOCRRuntime(); runtime != nil {
		runtime.wakeWorker()
	}
	writeJSON(c, gin.H{"ok": true, "local_service": manager.Snapshot(c.Request.Context())})
}

func (s *Service) handleOCRLocalServiceStop(c *gin.Context) {
	manager := s.currentOCRLocalService()
	if manager == nil || !manager.Managed() {
		c.JSON(http.StatusConflict, gin.H{"error": "当前 OCR 配置未使用本机托管服务"})
		return
	}
	stopContext, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if err := manager.Stop(stopContext); err != nil {
		chaterrors.Err(c, err)
		return
	}
	manager.SetAutoRestart(false)
	config, err := s.setOCRLocalAutoRestart(false)
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	writeJSON(c, gin.H{
		"ok":                 true,
		"local_auto_restart": config.LocalAutoRestartEnabled(),
		"local_service":      manager.Snapshot(c.Request.Context()),
	})
}

func (s *Service) setOCRLocalAutoRestart(enabled bool) (conf.OCRConfig, error) {
	storage, ok := s.conf.(OCRConfigStorage)
	if !ok {
		return conf.OCRConfig{}, errors.New("当前启动方式未提供 OCR 配置持久化能力")
	}
	config := storage.GetStoredOCRConfig()
	config.LocalAutoRestart = boolPointer(enabled)
	config = conf.NormalizeOCRConfig(config)
	if err := storage.UpdateOCRConfig(config); err != nil {
		return conf.OCRConfig{}, err
	}
	return config, nil
}

func (s *Service) handleOCRSearch(c *gin.Context) {
	keyword := strings.TrimSpace(c.Query("keyword"))
	if keyword == "" {
		chaterrors.Err(c, chaterrors.InvalidArg("keyword"))
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 20, 1, 500)
	if err != nil {
		chaterrors.Err(c, chaterrors.InvalidArg("limit"))
		return
	}
	offset, err := parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 5000)
	if err != nil {
		chaterrors.Err(c, chaterrors.InvalidArg("offset"))
		return
	}
	start, end, _, err := parseSinceUntil(c.Query("time"), c.Query("since"), c.Query("until"))
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OCR index is not initialized"})
		return
	}
	result, err := runtime.store.Search(c.Request.Context(), ocr.SearchRequest{
		Keyword: keyword,
		Talkers: util.Str2List(c.Query("chats"), ","),
		Since:   unixOrZero(start),
		Until:   unixOrZero(end),
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	messages := make([]gin.H, 0, len(result.Records))
	for _, record := range result.Records {
		message := ocrRecordMessage(record)
		row := toHistoryMessage(message, c.Request.Host)
		preferOCRMediaPath(row, record, c.Request.Host)
		row["chat"] = firstNonEmpty(record.TalkerName, record.Talker)
		row["username"] = record.Talker
		row["relevance"] = ocrRelevance(record.Rank)
		messages = append(messages, row)
	}
	c.Set(databaseQueryRowsContextKey, len(messages))
	c.Set(databaseQueryPathContextKey, result.Path)
	writeJSON(c, searchResponse{
		TotalCount: result.Total,
		Count:      len(messages),
		Limit:      limit,
		Offset:     offset,
		SearchPath: result.Path,
		Match:      "substring",
		Sort:       "relevance",
		Messages:   toHistoryMessageOutList(messages),
	})
}

func (s *Service) handleOCRBackfill(c *gin.Context) {
	request := struct {
		Limit        int      `json:"limit"`
		ReceivedOnly *bool    `json:"received_only"`
		RespectScope *bool    `json:"respect_scope"`
		Talkers      []string `json:"talkers"`
	}{Limit: 1000}
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		chaterrors.Err(c, chaterrors.InvalidArg("body"))
		return
	}
	if request.Limit < 1 || request.Limit > 10_000 {
		chaterrors.Err(c, chaterrors.InvalidArg("limit"))
		return
	}
	receivedOnly := s.configuredOCR().ReceivedOnly
	if request.ReceivedOnly != nil {
		receivedOnly = *request.ReceivedOnly
	}
	config := s.configuredOCR()
	if !config.BackfillEnabled {
		c.JSON(http.StatusConflict, gin.H{"error": "请先开启历史补录"})
		return
	}
	respectScope := true
	if request.RespectScope != nil {
		respectScope = *request.RespectScope
	}
	talkers := make([]string, 0, len(request.Talkers))
	for _, talker := range request.Talkers {
		if talker = strings.TrimSpace(talker); talker != "" {
			talkers = append(talkers, talker)
		}
	}
	backfillContext, err := s.beginOCRBackfill(
		c.Request.Context(),
		request.Limit,
		receivedOnly,
		respectScope,
		talkers,
	)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	scanned, enqueued, err := s.enqueueOCRBackfill(
		backfillContext,
		request.Limit,
		receivedOnly,
		respectScope,
		talkers,
	)
	s.finishOCRBackfill(scanned, enqueued, err)
	if err != nil && !errors.Is(err, context.Canceled) {
		chaterrors.Err(c, err)
		return
	}
	writeJSON(c, gin.H{
		"scanned":       scanned,
		"enqueued":      enqueued,
		"received_only": receivedOnly,
		"respect_scope": respectScope,
		"talkers":       talkers,
		"limit":         request.Limit,
		"status":        s.ocrStatus(c.Request.Context()),
	})
}

func (s *Service) handleOCRBackfillStop(c *gin.Context) {
	writeJSON(c, gin.H{
		"stopped": s.cancelOCRBackfill(),
		"status":  s.ocrStatus(c.Request.Context()),
	})
}

func (s *Service) handleOCRRecordGet(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		chaterrors.Err(c, chaterrors.InvalidArg("id"))
		return
	}
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OCR index is not initialized"})
		return
	}
	record, err := runtime.store.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "OCR record not found"})
			return
		}
		chaterrors.Err(c, err)
		return
	}
	if record == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "OCR record not found"})
		return
	}
	writeJSON(c, ocrRecordDetail(record))
}

func ocrRecordDetail(record *ocr.Record) gin.H {
	if record == nil {
		return gin.H{}
	}
	mediaKey := strings.TrimSpace(record.MediaPath)
	if mediaKey == "" {
		mediaKey = strings.TrimSpace(record.MediaKey)
	}
	detail := gin.H{
		"id":                   record.ID,
		"talker":               record.Talker,
		"talker_name":          record.TalkerName,
		"sender":               record.Sender,
		"sender_name":          record.SenderName,
		"is_self":              record.IsSelf,
		"message_time":         record.MessageTime,
		"message_seq":          record.MessageSeq,
		"db_local_id":          record.DBLocalID,
		"message_id":           record.MessageID,
		"media_key":            record.MediaKey,
		"media_path":           record.MediaPath,
		"content_hash":         record.ContentHash,
		"provider":             record.Provider,
		"model":                record.Model,
		"description":          record.Description,
		"ocr_text":             record.OCRText,
		"markdown":             record.Markdown,
		"status":               record.Status,
		"error":                record.Error,
		"attempts":             record.Attempts,
		"next_attempt_at":      record.NextAttemptAt,
		"media_retry_attempts": record.MediaRetryAttempts,
		"media_next_retry_at":  record.MediaNextRetryAt,
		"media_last_retry_at":  record.MediaLastRetryAt,
		"media_retry_error":    record.MediaRetryError,
		"created_at":           record.CreatedAt,
		"updated_at":           record.UpdatedAt,
	}
	if mediaKey != "" {
		detail["image_url"] = buildMediaPath("image", mediaKey)
	}
	if len(record.Layout) > 0 {
		var layout any
		if json.Unmarshal(record.Layout, &layout) == nil {
			detail["layout"] = layout
		}
	}
	return detail
}

func (s *Service) handleOCRSucceededClear(c *gin.Context) {
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OCR index is not initialized"})
		return
	}
	removed, err := runtime.store.ClearSucceeded(c.Request.Context())
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	writeJSON(c, gin.H{"removed": removed, "status": s.ocrStatus(c.Request.Context())})
}

func (s *Service) handleOCRRetry(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		chaterrors.Err(c, chaterrors.InvalidArg("id"))
		return
	}
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OCR index is not initialized"})
		return
	}
	retried, err := runtime.store.Retry(c.Request.Context(), id)
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	if retried {
		runtime.wakeWorker()
	}
	writeJSON(c, gin.H{"id": id, "retried": retried})
}

func (s *Service) ensureOCRMediaRetryHook(ctx context.Context) error {
	if !imgdownload.FridaAvailable() {
		return errors.New("本机图片下载 Hook 未就绪")
	}
	statusCtx, statusCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	status := imgdownload.DaemonStatus(statusCtx)
	statusCancel()
	if attached, _ := status["attached"].(bool); !attached {
		prewarmCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := imgdownload.PrewarmScopedFridaDaemon(prewarmCtx)
		cancel()
		if err != nil {
			return err
		}
	}
	configureCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return imgdownload.SetScopedDownloadTalkers(configureCtx, ocrScopedTalkers(s.configuredOCR()))
}

func (s *Service) handleOCRWaitingMediaRetry(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		chaterrors.Err(c, chaterrors.InvalidArg("id"))
		return
	}
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OCR index is not initialized"})
		return
	}
	record, err := runtime.store.Get(c.Request.Context(), id)
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	if record == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "OCR record not found"})
		return
	}
	if record.Status != ocr.StatusWaitingMedia {
		c.JSON(http.StatusConflict, gin.H{"error": "该记录当前不在等待中图状态", "id": id, "status": record.Status})
		return
	}
	if ready, _ := s.ocrImageReady(record.ImageRef); !ready {
		if err := s.ensureOCRMediaRetryHook(c.Request.Context()); err != nil {
			chaterrors.Err(c, err)
			return
		}
	}
	result, err := s.retryOCRWaitingMedia(runtime, []ocr.Record{*record}, true)
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	runtime.wakeMediaGate()
	writeJSON(c, gin.H{"id": id, "result": result, "status": s.ocrStatus(c.Request.Context())})
}

func (s *Service) handleOCRWaitingMediaRetryAll(c *gin.Context) {
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OCR index is not initialized"})
		return
	}
	request := struct {
		Limit int `json:"limit"`
	}{Limit: ocrMediaRetryBatchLimit}
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		chaterrors.Err(c, chaterrors.InvalidArg("body"))
		return
	}
	if request.Limit < 1 || request.Limit > ocrMediaRetryBatchLimit {
		chaterrors.Err(c, chaterrors.InvalidArg("limit"))
		return
	}
	records, err := runtime.store.WaitingMediaRecords(c.Request.Context(), request.Limit)
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	if len(records) > 0 {
		needsHook := false
		for _, record := range records {
			if ready, _ := s.ocrImageReady(record.ImageRef); !ready {
				needsHook = true
				break
			}
		}
		if needsHook {
			if err := s.ensureOCRMediaRetryHook(c.Request.Context()); err != nil {
				chaterrors.Err(c, err)
				return
			}
		}
	}
	result, err := s.retryOCRWaitingMedia(runtime, records, true)
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	runtime.wakeMediaGate()
	writeJSON(c, gin.H{"result": result, "status": s.ocrStatus(c.Request.Context())})
}

func (s *Service) handleOCRRerecognize(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		chaterrors.Err(c, chaterrors.InvalidArg("id"))
		return
	}
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OCR index is not initialized"})
		return
	}
	config := s.configuredOCR()
	requeued, err := runtime.store.Reprocess(c.Request.Context(), id, config.Provider, config.Model)
	if err != nil {
		chaterrors.Err(c, err)
		return
	}
	if requeued {
		runtime.wakeWorker()
	}
	writeJSON(c, gin.H{
		"id":       id,
		"requeued": requeued,
		"provider": config.Provider,
		"model":    config.Model,
	})
}

// preferOCRMediaPath keeps the message MD5 as a fallback, but makes the exact
// file path used by OCR the primary preview URL. An OCR result may be backed by
// a valid image file even when the media database has no matching MD5 row.
func preferOCRMediaPath(row gin.H, record ocr.Record, host string) {
	mediaPath := strings.TrimSpace(record.MediaPath)
	if mediaPath == "" {
		return
	}
	mediaKeys := []string{mediaPath}
	if mediaKey := strings.TrimSpace(record.MediaKey); mediaKey != "" && mediaKey != mediaPath {
		mediaKeys = append(mediaKeys, mediaKey)
	}
	path := buildMediaPath("image", mediaPath)
	row["media_type"] = "image"
	row["media_key"] = mediaPath
	row["media_keys"] = mediaKeys
	row["media_path"] = path
	row["image_key"] = mediaPath
	row["image_keys"] = mediaKeys
	row["image_path"] = path
	if host = strings.TrimSpace(host); host != "" {
		mediaURL := "http://" + host + path
		row["media_url"] = mediaURL
		row["image_url"] = mediaURL
	}
}

func ocrRecordMessage(record ocr.Record) *model.Message {
	contents := map[string]interface{}{
		"md5":               record.MediaKey,
		"path":              record.MediaPath,
		"image_description": record.Description,
		"ocr_text":          record.OCRText,
		"ocr_markdown":      record.Markdown,
		"ocr_provider":      record.Provider,
		"ocr_model":         record.Model,
		"ocr_status":        record.Status,
		"ocr_index_id":      record.ID,
	}
	if len(record.Layout) > 0 {
		var layout any
		if json.Unmarshal(record.Layout, &layout) == nil {
			contents["ocr_layout"] = layout
		}
	}
	return &model.Message{
		Seq:        record.MessageSeq,
		ID:         record.MessageID,
		DBLocalID:  record.DBLocalID,
		Time:       time.Unix(record.MessageTime, 0),
		Talker:     record.Talker,
		TalkerName: record.TalkerName,
		Sender:     record.Sender,
		SenderName: record.SenderName,
		IsSelf:     record.IsSelf,
		Type:       model.MessageTypeImage,
		Content:    record.Description,
		Contents:   contents,
	}
}

func ocrRelevance(rank float64) int {
	if rank >= 0 {
		return 1
	}
	score := int(-rank * 1000)
	if score < 1 {
		return 1
	}
	if score > 1_000_000 {
		return 1_000_000
	}
	return score
}
