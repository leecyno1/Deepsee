package http

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/ocr"
)

func TestOCRMediaRetryAllowTTLCoversPersistedDeadline(t *testing.T) {
	now := time.Unix(1_000, 0)
	if got := ocrMediaRetryAllowTTL(ocr.Record{}, now); got != 10*time.Minute {
		t.Fatalf("initial allow TTL=%v", got)
	}
	record := ocr.Record{MediaRetryAttempts: 4, MediaNextRetryAt: now.Add(time.Hour).Unix()}
	if got := ocrMediaRetryAllowTTL(record, now); got != 62*time.Minute {
		t.Fatalf("persisted allow TTL=%v", got)
	}
}

func TestOCRImagePathUsableAlwaysRejectsThumbnail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture_t.dat")
	if err := os.WriteFile(path, make([]byte, 200*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	if ocrImagePathUsable(path) {
		t.Fatal("thumb-named payload must be excluded regardless of size")
	}
	converted := filepath.Join(t.TempDir(), "fixture_t.jpg")
	if err := os.WriteFile(converted, make([]byte, 200*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	if ocrImagePathUsable(converted) {
		t.Fatal("converted thumbnail must remain excluded")
	}
	mid := filepath.Join(t.TempDir(), "fixture.dat")
	if err := os.WriteFile(mid, []byte("mid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ocrImagePathUsable(mid) {
		t.Fatal("mid image should be eligible")
	}
}

func TestAutoDownloadSuccessCountSurvivesChecking(t *testing.T) {
	rt := &imageOCRRuntime{}
	// Open attempt (as preferOCRImagePath does before EnsureOriginal).
	rt.setAutoDownload("triggering", "trigger", "mk", "tk", 1, true)
	if rt.autoDownloadTriggered != 1 || !rt.autoDownloadInFlight {
		t.Fatalf("after trigger: triggered=%d inFlight=%v", rt.autoDownloadTriggered, rt.autoDownloadInFlight)
	}
	// EnsureOriginal always emits checking next — must not kill the attempt.
	rt.setAutoDownload("checking", "check local", "mk", "tk", 1, false)
	if !rt.autoDownloadInFlight || !rt.autoDownloadActive {
		t.Fatalf("checking cleared attempt: inFlight=%v active=%v", rt.autoDownloadInFlight, rt.autoDownloadActive)
	}
	if rt.autoDownloadTriggered != 1 {
		t.Fatalf("checking re-counted trigger: %d", rt.autoDownloadTriggered)
	}
	rt.setAutoDownload("waiting", "wait file", "mk", "tk", 1, true)
	rt.setAutoDownload("ready", "got mid", "mk", "tk", 1, false)
	if rt.autoDownloadSucceeded != 1 {
		t.Fatalf("succeeded=%d want 1", rt.autoDownloadSucceeded)
	}
	if rt.autoDownloadInFlight || rt.autoDownloadActive {
		t.Fatalf("ready should close attempt: inFlight=%v active=%v", rt.autoDownloadInFlight, rt.autoDownloadActive)
	}
	// Idempotent second ready must not double-count.
	rt.setAutoDownload("ready", "finish ready", "mk", "tk", 1, false)
	if rt.autoDownloadSucceeded != 1 {
		t.Fatalf("double ready counted: succeeded=%d", rt.autoDownloadSucceeded)
	}
}

func TestAutoDownloadLocalDoesNotCountSuccess(t *testing.T) {
	rt := &imageOCRRuntime{}
	rt.setAutoDownload("local", "本地已有可用图片（mid）", "mk", "tk", 0, false)
	if rt.autoDownloadSucceeded != 0 || rt.autoDownloadTriggered != 0 {
		t.Fatalf("local should not count: triggered=%d succeeded=%d", rt.autoDownloadTriggered, rt.autoDownloadSucceeded)
	}
	if rt.autoDownloadStage != "local" {
		t.Fatalf("stage=%q", rt.autoDownloadStage)
	}
}

func TestAutoDownloadErrorClosesAttemptOnce(t *testing.T) {
	rt := &imageOCRRuntime{}
	rt.setAutoDownload("triggering", "t", "mk", "tk", 1, true)
	rt.setAutoDownload("error", "no_manager", "mk", "tk", 1, false)
	if rt.autoDownloadFailed != 1 || rt.autoDownloadInFlight {
		t.Fatalf("failed=%d inFlight=%v", rt.autoDownloadFailed, rt.autoDownloadInFlight)
	}
	rt.setAutoDownload("error", "no_manager again", "mk", "tk", 1, false)
	if rt.autoDownloadFailed != 1 {
		t.Fatalf("double error counted: failed=%d", rt.autoDownloadFailed)
	}
}
