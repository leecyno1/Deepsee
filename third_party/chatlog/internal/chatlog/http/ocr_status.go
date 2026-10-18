package http

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/wechat/imgdownload"
)

// reclaimStaleAutoDownload clears a stuck active download while status is read.
func (runtime *imageOCRRuntime) reclaimStaleAutoDownload(maxAge time.Duration) {
	if runtime == nil || maxAge <= 0 {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.autoDownloadActive || runtime.autoDownloadStartedAt <= 0 {
		return
	}
	started := time.Unix(runtime.autoDownloadStartedAt, 0)
	if time.Since(started) < maxAge {
		return
	}
	detail := fmt.Sprintf("原图下载状态超时（已超过 %s），已自动复位", maxAge.Round(time.Second))
	if runtime.autoDownloadInFlight || runtime.autoDownloadActive {
		runtime.setAutoDownloadLocked("error", detail, runtime.autoDownloadMediaKey, runtime.autoDownloadTalker, runtime.autoDownloadRecordID, false)
	}
	if runtime.currentRecordID != 0 && runtime.currentStartedAt > 0 &&
		time.Since(time.Unix(runtime.currentStartedAt, 0)) >= maxAge {
		runtime.clearCurrentTaskLocked()
	}
}

type imageOCRScopeStatus struct {
	All       bool     `json:"all"`
	Contacts  []string `json:"contacts"`
	ChatRooms []string `json:"chatrooms"`
}

type imageOCRCurrentStatus struct {
	RecordID  int64  `json:"record_id,omitempty"`
	Talker    string `json:"talker,omitempty"`
	MediaKey  string `json:"media_key,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
	Stage     string `json:"stage,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type imageOCRAutoDownloadStatus struct {
	Enabled    bool   `json:"enabled"`
	Available  bool   `json:"available"`
	Active     bool   `json:"active"`
	Stage      string `json:"stage,omitempty"`
	Detail     string `json:"detail,omitempty"`
	RecordID   int64  `json:"record_id,omitempty"`
	Talker     string `json:"talker,omitempty"`
	MediaKey   string `json:"media_key,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	Triggered  uint64 `json:"triggered"`
	Succeeded  uint64 `json:"succeeded"`
	Failed     uint64 `json:"failed"`
	Skipped    uint64 `json:"skipped"`
	// Resident Frida helper (long-lived attach + receive-request upgrade).
	FridaDaemon map[string]interface{} `json:"frida_daemon,omitempty"`
}

type imageOCRBackfillStatus struct {
	Enabled      bool     `json:"enabled"`
	Running      bool     `json:"running"`
	StartedAt    int64    `json:"started_at,omitempty"`
	FinishedAt   int64    `json:"finished_at,omitempty"`
	Limit        int      `json:"limit,omitempty"`
	ReceivedOnly bool     `json:"received_only"`
	RespectScope bool     `json:"respect_scope"`
	Talkers      []string `json:"talkers,omitempty"`
	Scanned      int      `json:"scanned"`
	Enqueued     int      `json:"enqueued"`
	Error        string   `json:"error,omitempty"`
}

type imageOCRStatus struct {
	Enabled            bool                       `json:"enabled"`
	Running            bool                       `json:"running"`
	RealtimeRunning    bool                       `json:"realtime_running"`
	WorkerRunning      bool                       `json:"worker_running"`
	Mode               string                     `json:"mode"`
	Provider           string                     `json:"provider"`
	Model              string                     `json:"model"`
	Endpoint           string                     `json:"endpoint"`
	APIKeyConfigured   bool                       `json:"api_key_configured"`
	ReceivedOnly       bool                       `json:"received_only"`
	Scope              imageOCRScopeStatus        `json:"scope"`
	BackfillOnStart    bool                       `json:"backfill_on_start"`
	Backfill           imageOCRBackfillStatus     `json:"backfill"`
	PollInterval       int                        `json:"poll_interval_sec"`
	TriggerMode        string                     `json:"trigger_mode"`
	RequestTimeout     int                        `json:"request_timeout_sec"`
	StartedAt          int64                      `json:"started_at,omitempty"`
	LastScanAt         int64                      `json:"last_scan_at,omitempty"`
	LastScanDurationMS int64                      `json:"last_scan_duration_ms"`
	LastScanMessages   uint64                     `json:"last_scan_messages"`
	LastScanEnqueued   uint64                     `json:"last_scan_enqueued"`
	LastSuccessAt      int64                      `json:"last_success_at,omitempty"`
	LastErrorAt        int64                      `json:"last_error_at,omitempty"`
	LastError          string                     `json:"last_error,omitempty"`
	Scanned            uint64                     `json:"scanned"`
	Enqueued           uint64                     `json:"enqueued"`
	Processed          uint64                     `json:"processed"`
	Failed             uint64                     `json:"failed"`
	Current            imageOCRCurrentStatus      `json:"current"`
	AutoDownload       imageOCRAutoDownloadStatus `json:"auto_download"`
	Index              ocr.Stats                  `json:"index"`
	Recent             []ocr.RecordSummary        `json:"recent"`
	LocalService       ocr.LocalServiceSnapshot   `json:"local_service"`
}

func (s *Service) ocrStatus(ctx context.Context) imageOCRStatus {
	config := s.configuredOCR()
	mode := "local"
	if config.Provider == conf.OCRProviderMaaS {
		mode = "api"
	}
	status := imageOCRStatus{
		Enabled:          config.Enabled,
		Mode:             mode,
		Provider:         config.Provider,
		Model:            config.Model,
		Endpoint:         config.Endpoint,
		APIKeyConfigured: strings.TrimSpace(config.APIKey) != "",
		ReceivedOnly:     config.ReceivedOnly,
		Scope: imageOCRScopeStatus{
			All:       config.ScopeAll(),
			Contacts:  config.ListenContactsList(),
			ChatRooms: config.ListenChatRoomsList(),
		},
		BackfillOnStart: config.BackfillOnStart,
		Backfill:        s.currentOCRBackfillStatus(config),
		PollInterval:    config.PollIntervalSec,
		TriggerMode:     "database_event",
		RequestTimeout:  config.RequestTimeout,
		AutoDownload: imageOCRAutoDownloadStatus{
			Enabled:   config.AutoDownloadOriginal || strings.TrimSpace(os.Getenv("CHATLOG_IMAGE_DOWNLOAD_TRIGGER")) == "1",
			Available: s.ocrAutoDownloadAvailable(),
		},
	}
	if provider, ok := s.db.(MessageChangeStatusProvider); ok && provider.MessageChangeStats().Running {
		status.TriggerMode = "shared_message_change_stream"
	}
	if manager := s.currentOCRLocalService(); manager != nil {
		probeContext, cancel := context.WithTimeout(ctx, time.Second)
		status.LocalService = manager.Snapshot(probeContext)
		cancel()
	} else {
		status.LocalService = ocr.NewLocalServiceManager(config.Endpoint, ocr.LocalServiceOptions{
			AutoStart:   config.LocalAutoStartEnabled(),
			AutoRestart: config.LocalAutoRestartEnabled(),
		}).Snapshot(ctx)
	}
	runtime := s.currentOCRRuntime()
	if runtime == nil {
		return status
	}
	// Self-heal stuck "triggering/waiting" panels if a Frida worker never finished.
	runtime.reclaimStaleAutoDownload(75 * time.Second)
	runtime.mu.RLock()
	status.RealtimeRunning = runtime.scannerRunning
	status.WorkerRunning = runtime.workerRunning
	status.Running = runtime.scannerRunning || runtime.workerRunning
	status.StartedAt = runtime.startedAt
	status.LastScanAt = runtime.lastScanAt
	status.LastScanDurationMS = runtime.lastScanDurationMS
	status.LastScanMessages = runtime.lastScanMessages
	status.LastScanEnqueued = runtime.lastScanEnqueued
	status.LastSuccessAt = runtime.lastSuccessAt
	status.LastErrorAt = runtime.lastErrorAt
	status.LastError = runtime.lastError
	status.Scanned = runtime.scanned
	status.Enqueued = runtime.enqueued
	status.Processed = runtime.processed
	status.Failed = runtime.failed
	status.Current = imageOCRCurrentStatus{
		RecordID:  runtime.currentRecordID,
		Talker:    runtime.currentTalker,
		MediaKey:  runtime.currentMediaKey,
		StartedAt: runtime.currentStartedAt,
		Stage:     runtime.currentStage,
		Detail:    runtime.currentDetail,
	}
	status.AutoDownload.Active = runtime.autoDownloadActive
	status.AutoDownload.Stage = runtime.autoDownloadStage
	status.AutoDownload.Detail = runtime.autoDownloadDetail
	status.AutoDownload.RecordID = runtime.autoDownloadRecordID
	status.AutoDownload.Talker = runtime.autoDownloadTalker
	status.AutoDownload.MediaKey = runtime.autoDownloadMediaKey
	status.AutoDownload.StartedAt = runtime.autoDownloadStartedAt
	status.AutoDownload.FinishedAt = runtime.autoDownloadFinishedAt
	status.AutoDownload.LastError = runtime.autoDownloadLastError
	status.AutoDownload.Triggered = runtime.autoDownloadTriggered
	status.AutoDownload.Succeeded = runtime.autoDownloadSucceeded
	status.AutoDownload.Failed = runtime.autoDownloadFailed
	status.AutoDownload.Skipped = runtime.autoDownloadSkipped
	runtime.mu.RUnlock()
	// Best-effort daemon snapshot (short timeout; never blocks OCR worker).
	if status.AutoDownload.Enabled && status.AutoDownload.Available {
		daemonCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		status.AutoDownload.FridaDaemon = imgdownload.DaemonStatus(daemonCtx)
		cancel()
	}
	if runtime.store != nil {
		indexStats, err := runtime.store.Stats(ctx)
		if err != nil {
			runtime.setError(err)
		} else {
			status.Index = indexStats
		}
		recent, err := runtime.store.Recent(ctx, 20)
		if err != nil {
			runtime.setError(err)
		} else {
			for index := range recent {
				if strings.TrimSpace(recent[index].Provider) == "" {
					recent[index].Provider = config.Provider
				}
				if strings.TrimSpace(recent[index].Model) == "" {
					recent[index].Model = config.Model
				}
			}
			status.Recent = recent
		}
	}
	return status
}
