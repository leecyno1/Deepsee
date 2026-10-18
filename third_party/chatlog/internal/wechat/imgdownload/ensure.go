package imgdownload

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNotConfigured means no native download trigger is wired yet.
	ErrNotConfigured = errors.New("original image download trigger is not configured")
	// ErrOnlyThumbnail means local storage still only has a thumbnail after wait.
	ErrOnlyThumbnail = errors.New("only thumbnail is available for image")
	// ErrTimeout means the original did not land before the deadline.
	ErrTimeout = errors.New("timed out waiting for original image")
)

// Request describes a local image that may need an original/HD download.
type Request struct {
	DataDir      string
	MediaKey     string
	MediaPath    string // relative base path from message/hardlink when known
	Talker       string // native request identity for replaying an existing message
	MessageTime  int64
	DBLocalID    int64
	MinTier      int // default TierMid
	Timeout      time.Duration
	PollEvery    time.Duration
	AllowTrigger bool // when false, only scan local files
	CDN          CDNFields
	// WaitFile is an optional absolute path the trigger may poll after invoke.
	WaitFile string
}

// Result is the best local file after ensure.
type Result struct {
	Path      string
	Tier      int
	Size      int64
	Triggered bool
	Waited    time.Duration
}

// Trigger downloads (or requests) the original image via WeChat-native means
// (Frida hook / future IPC). Implementations must be safe to call repeatedly.
type Trigger interface {
	// RequestDownload asks WeChat to fetch a higher-quality image for the key.
	// It should return quickly after queueing work; Ensure polls the filesystem.
	RequestDownload(ctx context.Context, req Request) error
}

// ProgressFunc receives stage updates during EnsureOriginal.
// stage examples: checking, triggering, waiting, ready, error, skipped.
type ProgressFunc func(stage, detail string)

// Ensurer resolves a good-enough local image, optionally triggering download.
type Ensurer struct {
	Trigger  Trigger
	Progress ProgressFunc
}

func (e Ensurer) progress(stage, detail string) {
	if e.Progress != nil {
		e.Progress(stage, detail)
	}
}

// EnsureOriginal returns a local path with tier >= MinTier when possible.
//
// Without a Trigger, it only re-scans the filesystem. Realtime receive upgrade
// is handled before this legacy trigger path.
func (e Ensurer) EnsureOriginal(ctx context.Context, req Request) (Result, error) {
	if req.MinTier <= TierNone {
		req.MinTier = TierMid
	}
	if req.Timeout <= 0 {
		req.Timeout = 45 * time.Second
	}
	if req.PollEvery <= 0 {
		req.PollEvery = 500 * time.Millisecond
	}

	start := time.Now()
	e.progress("checking", "检查本地是否已有中图/高清")
	if best, ok := scan(req); ok && best.Tier >= req.MinTier {
		e.progress("ready", fmt.Sprintf("本地已有 %s", TierName(best.Tier)))
		return Result{Path: best.Path, Tier: best.Tier, Size: best.Size, Waited: time.Since(start)}, nil
	}

	triggered := false
	if req.AllowTrigger {
		if e.Trigger == nil {
			e.progress("skipped", "未配置自动下载触发器")
			return Result{}, fmt.Errorf("%w: media_key=%s", ErrNotConfigured, req.MediaKey)
		}
		e.progress("triggering", "正在请求微信下载中图/高清")
		// Always hard-cap the native invoke so OCR never stays on "triggering"
		// even when the parent ctx has a multi-minute OCR request timeout.
		invokeTimeout := 15 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			if rem := time.Until(deadline); rem > 0 && rem < invokeTimeout {
				invokeTimeout = rem
			}
		}
		invokeCtx, cancel := context.WithTimeout(ctx, invokeTimeout)
		err := e.Trigger.RequestDownload(invokeCtx, req)
		cancel()
		if err != nil {
			e.progress("error", err.Error())
			return Result{}, err
		}
		triggered = true
		e.progress("waiting", "已触发下载，等待文件落盘")
	}

	deadline := time.Now().Add(req.Timeout)
	ticker := time.NewTicker(req.PollEvery)
	defer ticker.Stop()

	for {
		if best, ok := scan(req); ok && best.Tier >= req.MinTier {
			e.progress("ready", fmt.Sprintf("已获得 %s（%d 字节）", TierName(best.Tier), best.Size))
			return Result{
				Path:      best.Path,
				Tier:      best.Tier,
				Size:      best.Size,
				Triggered: triggered,
				Waited:    time.Since(start),
			}, nil
		}
		if !triggered {
			// No trigger and no poll loop usefulness beyond first scan.
			if best, ok := scan(req); ok {
				e.progress("error", fmt.Sprintf("仅有 %s", TierName(best.Tier)))
				return Result{Path: best.Path, Tier: best.Tier, Size: best.Size, Waited: time.Since(start)}, fmt.Errorf("%w: tier=%s path=%s", ErrOnlyThumbnail, TierName(best.Tier), best.Path)
			}
			e.progress("error", "本地未找到可用图片")
			return Result{}, fmt.Errorf("%w: media_key=%s", ErrOnlyThumbnail, req.MediaKey)
		}
		if time.Now().After(deadline) {
			if best, ok := scan(req); ok {
				e.progress("error", fmt.Sprintf("等待超时，当前仍为 %s", TierName(best.Tier)))
				return Result{Path: best.Path, Tier: best.Tier, Size: best.Size, Triggered: triggered, Waited: time.Since(start)}, fmt.Errorf("%w: tier=%s", ErrTimeout, TierName(best.Tier))
			}
			e.progress("error", "等待超时，原图仍未落地")
			return Result{Triggered: triggered, Waited: time.Since(start)}, fmt.Errorf("%w: media_key=%s", ErrTimeout, req.MediaKey)
		}
		waited := time.Since(start).Round(time.Second)
		e.progress("waiting", fmt.Sprintf("等待文件落盘中… 已等待 %s", waited))
		select {
		case <-ctx.Done():
			e.progress("error", ctx.Err().Error())
			return Result{Triggered: triggered, Waited: time.Since(start)}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func scan(req Request) (Candidate, bool) {
	return BestCandidate(CollectCandidates(req.DataDir, req.MediaKey, req.MediaPath))
}

// ScanBest is a convenience local-only lookup.
func ScanBest(dataDir, mediaKey, mediaPath string) (Candidate, bool) {
	return BestCandidate(CollectCandidates(dataDir, mediaKey, mediaPath))
}
