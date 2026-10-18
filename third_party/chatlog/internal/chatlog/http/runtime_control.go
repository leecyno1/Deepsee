package http

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/wechat/imgdownload"
)

type runtimeOSProcess struct {
	PID     int
	PPID    int
	Command string
}

func relatedRuntimeProcess(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"wechat_image_download_frida.py",
		"wechat_key_frida.py",
		"/.chatlog/ocr-runtime/",
		"mlx_vlm.server",
		"glmocr.server",
		"frida-helper",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	fields := strings.Fields(command)
	if len(fields) > 0 {
		base := strings.ToLower(filepath.Base(fields[0]))
		if base == "chatlog" || strings.HasPrefix(base, "chatlog-") {
			return true
		}
	}
	if strings.Contains(lower, "/go-build") && strings.Contains(lower, "/exe/chatlog") {
		return true
	}
	return strings.Contains(lower, "go run") && strings.Contains(lower, "ops server-start")
}

const runtimeTerminateConfirmation = "terminate-and-restart"

type runtimeTerminateRequest struct {
	Confirmation string `json:"confirmation"`
}

// handleRuntimeTerminate acknowledges the browser first, then tears down the
// runtime outside the request goroutine. This gives the response enough time to
// reach the dashboard before the HTTP listener and current process disappear.
func (s *Service) handleRuntimeTerminate(c *gin.Context) {
	var request runtimeTerminateRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Confirmation) != runtimeTerminateConfirmation {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "confirmation must equal " + runtimeTerminateConfirmation,
		})
		return
	}

	s.runtimeControlMu.Lock()
	if s.runtimeControlStarted {
		s.runtimeControlMu.Unlock()
		c.JSON(http.StatusAccepted, gin.H{
			"accepted": true,
			"pending":  true,
			"message":  "Chatlog 结束流程已在执行",
		})
		return
	}
	s.runtimeControlStarted = true
	delay := s.runtimeControlDelay
	if delay <= 0 {
		delay = 350 * time.Millisecond
	}
	runner := s.runtimeControlRunner
	if runner == nil {
		runner = s.terminateRuntimeAndRestartWeChat
	}
	s.runtimeControlMu.Unlock()

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, gin.H{
		"accepted": true,
		"pending":  true,
		"message":  "正在结束 Chatlog 及关联进程，释放注入并重启微信",
	})

	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		runner()
	}()
}

func (s *Service) terminateRuntimeAndRestartWeChat() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Stop the managed model servers while their manager and PID files are still
	// available. Continue with the remaining cleanup even if a stale PID makes
	// the stop script return an error.
	if manager := s.currentOCRLocalService(); manager != nil && manager.Managed() {
		stopCtx, stopCancel := context.WithTimeout(ctx, 15*time.Second)
		if err := manager.Stop(stopCtx); err != nil {
			log.Warn().Err(err).Msg("stop managed OCR processes during runtime termination")
		}
		stopCancel()
	}

	// Cancels scanners/workers and performs the tracked Frida detach.
	s.stopOCRRuntime()
	// Also covers a resident helper created outside the OCR runtime.
	imgdownload.StopFridaDaemon()

	terminated, err := terminateRelatedRuntimeProcesses(ctx)
	if err != nil {
		log.Warn().Err(err).Int("terminated", terminated).Msg("clean related Chatlog processes")
	} else {
		log.Info().Int("terminated", terminated).Msg("related Chatlog processes cleared")
	}

	wechatPID, err := restartWeChat(ctx)
	if err != nil {
		log.Error().Err(err).Msg("restart WeChat after Chatlog termination")
	} else {
		log.Info().Int("wechat_pid", wechatPID).Msg("WeChat restarted after Chatlog termination")
	}

	// Shutdown is last: in headless mode ListenAndServe returning lets the main
	// command exit, while TUI mode receives the explicit process-exit signal.
	if err := s.Stop(); err != nil {
		log.Warn().Err(err).Msg("stop HTTP service during runtime termination")
	}
	exitCurrentChatlogProcess()
}
