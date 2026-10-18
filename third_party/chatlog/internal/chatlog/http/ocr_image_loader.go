package http

import (
	"context"
	"fmt"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechat/imgdownload"
)

func imageOCRRef(message *model.Message) (ocr.ImageRef, bool) {
	if message == nil || message.Type != model.MessageTypeImage {
		return ocr.ImageRef{}, false
	}
	mediaType, keys := extractMediaRef(message)
	if mediaType != "image" || len(keys) == 0 {
		return ocr.ImageRef{}, false
	}
	mediaKey := strings.TrimSpace(toString(message.Contents["md5"]))
	mediaPath := strings.TrimSpace(toString(message.Contents["path"]))
	if mediaKey == "" && len(keys) > 0 {
		mediaKey = keys[0]
	}
	return ocr.ImageRef{
		Talker:       message.Talker,
		TalkerName:   message.TalkerName,
		Sender:       message.Sender,
		SenderName:   message.SenderName,
		IsSelf:       message.IsSelf,
		MessageTime:  message.Time.Unix(),
		MessageSeq:   message.Seq,
		DBLocalID:    message.DBLocalID,
		MessageID:    message.ID,
		MediaKey:     mediaKey,
		MediaPath:    mediaPath,
		AesKey:       strings.TrimSpace(toString(message.Contents["aeskey"])),
		CdnMidImgURL: strings.TrimSpace(toString(message.Contents["cdnmidimgurl"])),
		CdnBigImgURL: strings.TrimSpace(toString(message.Contents["cdnbigimgurl"])),
		CdnThumbURL:  strings.TrimSpace(toString(message.Contents["cdnthumburl"])),
		HdLength:     strings.TrimSpace(toString(message.Contents["hdlength"])),
		Length:       strings.TrimSpace(toString(message.Contents["length"])),
	}, mediaKey != "" || mediaPath != ""
}

func (s *Service) loadImageForOCR(ctx context.Context, ref ocr.ImageRef) ([]byte, string, error) {
	absolutePath := s.resolveOCRImagePath(ref)
	mediaPath := strings.TrimSpace(ref.MediaPath)
	mediaKey := strings.TrimSpace(ref.MediaKey)
	if absolutePath == "" {
		return nil, "", fmt.Errorf("OCR image file not found for %s", firstNonEmpty(mediaKey, mediaPath))
	}

	// Prefer mid/big/HD over thumb. With automatic receive upgrade enabled, the
	// normal .dat usually lands shortly after the initial *_t.dat.
	absolutePath = s.preferOCRImagePath(ctx, absolutePath, ref)
	if !ocrImagePathUsable(absolutePath) {
		return nil, "", fmt.Errorf(
			"OCR requires mid/original image for %s (path=%s); thumbnail was excluded",
			firstNonEmpty(mediaKey, mediaPath),
			filepath.Base(absolutePath),
		)
	}

	if converted := convertedImagePath(absolutePath); converted != "" {
		data, err := readLocalMediaFile(converted)
		if err != nil {
			return nil, "", err
		}
		return data, stdhttp.DetectContentType(data), nil
	}
	release, err := s.acquireLocalMediaSlot(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()
	data, err := readLocalMediaFile(absolutePath)
	if err != nil {
		return nil, "", err
	}
	detected := stdhttp.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") && !strings.HasSuffix(strings.ToLower(absolutePath), ".dat") {
		return data, detected, nil
	}
	decoded, ext, err := s.decodeMedia(data)
	if err != nil && s.shouldRetryImageDecryptAfterKeyRefresh(err) {
		if _, refreshErr := s.tryRefreshImageKeyFromWeChat(); refreshErr == nil {
			decoded, ext, err = s.decodeMedia(data)
		}
	}
	if err != nil {
		if strings.HasPrefix(detected, "image/") {
			return data, detected, nil
		}
		return nil, "", fmt.Errorf("decode OCR image %s: %w", filepath.Base(absolutePath), err)
	}
	// OCR consumes decoded bytes in memory. Persisting a plaintext sibling here
	// duplicates every selected image during a burst; media preview routes keep
	// their independent save/cache behavior.
	return decoded, contentTypeForImageExtension(ext, decoded), nil
}

// resolveOCRImagePath uses only direct repository/cache mappings. It never
// walks the account tree, which keeps the media gate cheap during bursts.
func (s *Service) resolveOCRImagePath(ref ocr.ImageRef) string {
	absolutePath := ""
	mediaPath := strings.TrimSpace(ref.MediaPath)
	mediaKey := strings.TrimSpace(ref.MediaKey)
	if mediaPath != "" {
		absolutePath = s.tryFindFileWithSuffixes("image", mediaPath)
		if absolutePath == "" {
			if candidate, _, err := s.safeDataPath(mediaPath); err == nil {
				if _, statErr := os.Stat(candidate); statErr == nil {
					absolutePath = candidate
				}
			}
		}
	}
	if mediaKey != "" {
		if media, err := s.db.GetMedia("image", mediaKey); err == nil && media != nil {
			if absolutePath == "" {
				absolutePath = s.tryFindFileWithSuffixes("image", media.Path)
			}
			if absolutePath == "" {
				absolutePath = s.findRelocatedMediaFile(media)
			}
		}
		if absolutePath == "" {
			if cached := s.getMD5FromCache(mediaKey); cached != "" {
				absolutePath = s.tryFindFileWithSuffixes("image", cached)
			}
		}
		// OCR runs from database events and therefore expects the message path,
		// media repository, or existing path cache to resolve the file. Do not
		// walk session/month trees here: one missing path in a burst must not turn
		// into repeated directory scans.
	}
	return absolutePath
}

// preferOCRImagePath upgrades a resolved thumb path to a better local variant.
// The resident Frida hook changes WeChat's live incoming request from thumbnail
// variant 3 to normal-image variant 2, so this function normally only needs to
// wait briefly for WeChat's own download to land the .dat file.
func (s *Service) preferOCRImagePath(ctx context.Context, absolutePath string, ref ocr.ImageRef) string {
	mediaKey := strings.TrimSpace(ref.MediaKey)
	mediaPath := strings.TrimSpace(ref.MediaPath)
	talker := strings.TrimSpace(ref.Talker)
	runtime := s.currentOCRRuntime()
	recordID := int64(0)
	if runtime != nil {
		runtime.mu.RLock()
		recordID = runtime.currentRecordID
		runtime.mu.RUnlock()
	}
	dataDir := ""
	if s.conf != nil {
		dataDir = strings.TrimSpace(s.conf.GetDataDir())
	}
	if dataDir == "" {
		return absolutePath
	}
	if best, ok := imgdownload.ScanBest(dataDir, mediaKey, mediaPath); ok {
		if absolutePath == "" || best.Tier > imgdownload.TierOfName(absolutePath) {
			absolutePath = best.Path
		}
	}
	if imgdownload.PreferableForOCR(imgdownload.TierOfName(absolutePath)) {
		if runtime != nil {
			// Stage "local" (not "ready"): already on disk, not a Frida download success.
			runtime.setAutoDownload("local", fmt.Sprintf("本地已有可用图片（%s）", imgdownload.TierName(imgdownload.TierOfName(absolutePath))), mediaKey, talker, recordID, false)
		}
		return absolutePath
	}
	if s.imageReceiveUpgradeEnabled() {
		if runtime != nil {
			runtime.setAutoDownload("waiting", "自动接收升级已命中，等待普通图片落盘…", mediaKey, talker, recordID, false)
		}
		deadline := time.NewTimer(2 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer deadline.Stop()
		defer ticker.Stop()
		for {
			if best, ok := imgdownload.ScanBest(dataDir, mediaKey, mediaPath); ok {
				if imgdownload.PreferableForOCR(best.Tier) {
					if runtime != nil {
						runtime.setAutoDownload("local", fmt.Sprintf("自动接收已落盘（%s · %d 字节）", imgdownload.TierName(best.Tier), best.Size), mediaKey, talker, recordID, false)
					}
					return best.Path
				}
			}
			select {
			case <-ctx.Done():
				return absolutePath
			case <-deadline.C:
				if runtime != nil {
					runtime.setAutoDownload("skipped", "普通图片暂未落盘，OCR 队列稍后自动重试", mediaKey, talker, recordID, false)
				}
				return absolutePath
			case <-ticker.C:
			}
		}
	}
	ensurer := imgdownload.Ensurer{Trigger: s.imageDownloadTrigger()}
	if ensurer.Trigger == nil {
		if runtime != nil {
			runtime.setAutoDownload("skipped", "自动触发原图下载未开启或本机 Frida 不可用", mediaKey, talker, recordID, false)
		}
		return absolutePath
	}
	cdn := imgdownload.CDNFields{
		AesKey:       strings.TrimSpace(ref.AesKey),
		CdnMidImgURL: strings.TrimSpace(ref.CdnMidImgURL),
		CdnBigImgURL: strings.TrimSpace(ref.CdnBigImgURL),
		CdnThumbURL:  strings.TrimSpace(ref.CdnThumbURL),
		HdLength:     strings.TrimSpace(ref.HdLength),
		Length:       strings.TrimSpace(ref.Length),
	}
	if !cdn.Usable() {
		log.Debug().Str("media_key", mediaKey).Msg("skip image download trigger: missing CDN metadata")
		if runtime != nil {
			runtime.setAutoDownload("skipped", "缺少 CDN 元数据（aeskey / cdn*url），无法自动触发下载", mediaKey, talker, recordID, false)
		}
		return absolutePath
	}

	// Always finish auto-download progress so the UI never sticks on "triggering".
	finished := false
	finish := func(stage, detail string, active bool) {
		finished = true
		if runtime != nil {
			runtime.setAutoDownload(stage, detail, mediaKey, talker, recordID, active)
		}
	}
	defer func() {
		if !finished && runtime != nil {
			runtime.setAutoDownload("error", "原图下载异常中断（状态已复位）", mediaKey, talker, recordID, false)
		}
	}()

	ensurer.Progress = func(stage, detail string) {
		if runtime == nil {
			return
		}
		switch stage {
		case "triggering", "waiting", "checking":
			// checking stays active while in-flight (see setAutoDownloadLocked).
			runtime.setAutoDownload(stage, detail, mediaKey, talker, recordID, true)
		case "ready", "error", "skipped":
			runtime.setAutoDownload(stage, detail, mediaKey, talker, recordID, false)
		default:
			runtime.setAutoDownload(stage, detail, mediaKey, talker, recordID, false)
		}
	}

	// Bound total ensure (invoke + filesystem wait) well below OCR request timeout.
	ensureTimeout := 35 * time.Second
	ensureCtx, cancel := context.WithTimeout(ctx, ensureTimeout)
	defer cancel()

	// Open the attempt once here so counters survive the subsequent "checking" stage.
	finish("triggering", "正在通过 Frida 请求微信下载中图/高清", true)
	res, err := ensurer.EnsureOriginal(ensureCtx, imgdownload.Request{
		DataDir:      dataDir,
		MediaKey:     mediaKey,
		MediaPath:    mediaPath,
		MinTier:      imgdownload.TierMid,
		AllowTrigger: true,
		Timeout:      20 * time.Second, // wait for file after invoke
		PollEvery:    400 * time.Millisecond,
		CDN:          cdn,
	})
	if err != nil {
		log.Debug().Err(err).Str("media_key", mediaKey).Msg("original image ensure failed")
		detail := err.Error()
		if res.Path != "" {
			detail = fmt.Sprintf("%s；当前仍为 %s", err.Error(), imgdownload.TierName(res.Tier))
		}
		// Progress may already have closed the attempt as error; finish is idempotent.
		finish("error", detail, false)
		if res.Path != "" && res.Tier > imgdownload.TierOfName(absolutePath) {
			return res.Path
		}
		return absolutePath
	}
	if res.Path != "" {
		detail := fmt.Sprintf("原图下载完成（%s · %d 字节）", imgdownload.TierName(res.Tier), res.Size)
		if res.Triggered {
			detail = fmt.Sprintf("已触发并完成下载（%s · %d 字节 · 等待 %s）", imgdownload.TierName(res.Tier), res.Size, res.Waited.Round(time.Millisecond))
		} else if res.Waited > 0 {
			detail = fmt.Sprintf("已获得可用图片（%s · %d 字节）", imgdownload.TierName(res.Tier), res.Size)
		}
		// If Progress already counted ready, this is a no-op for counters.
		// If Progress only reported checking→ready while inFlight, it already counted.
		// If Progress missed ready, this finish still closes the attempt as success.
		finish("ready", detail, false)
		return res.Path
	}
	finish("error", "原图下载未返回可用文件", false)
	return absolutePath
}

// ocrAutoDownloadAvailable reports whether this host can run the experimental
// Frida original-image download path.
func (s *Service) ocrAutoDownloadAvailable() bool {
	return imgdownload.FridaAvailable()
}

func (s *Service) imageReceiveUpgradeEnabled() bool {
	force := strings.TrimSpace(os.Getenv("CHATLOG_IMAGE_DOWNLOAD_TRIGGER")) == "1"
	config := s.configuredOCR()
	return (force || config.AutoDownloadOriginal) && imgdownload.FridaAvailable()
}

// imageDownloadTrigger returns a native WeChat download trigger when enabled.
// Prefer persisted OCR config auto_download_original; CHATLOG_IMAGE_DOWNLOAD_TRIGGER=1
// remains a force-enable override for ops.
func (s *Service) imageDownloadTrigger() imgdownload.Trigger {
	force := strings.TrimSpace(os.Getenv("CHATLOG_IMAGE_DOWNLOAD_TRIGGER")) == "1"
	if (!force && !s.configuredOCR().AutoDownloadOriginal) ||
		!imgdownload.FridaAvailable() {
		return nil
	}
	return imgdownload.NewFridaTrigger("")
}

// ocrImagePathUsable enforces the quality boundary immediately before bytes
// are read. Thumbnail suffixes remain excluded after conversion (`_t.jpg`).
func ocrImagePathUsable(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || imgdownload.IsThumbnailName(path) {
		return false
	}
	if imgdownload.PreferableForOCR(imgdownload.TierOfName(path)) {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".dat" {
		return false
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	default:
		return false
	}
}

func convertedImagePath(path string) string {
	lower := strings.ToLower(path)
	if !strings.HasSuffix(lower, ".dat") && filepath.Ext(path) != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		return ""
	}
	base := path
	if filepath.Ext(path) != "" {
		base = strings.TrimSuffix(path, filepath.Ext(path))
	}
	for _, extension := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"} {
		candidate := base + extension
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func contentTypeForImageExtension(extension string, data []byte) string {
	switch strings.ToLower(strings.TrimPrefix(extension, ".")) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return stdhttp.DetectContentType(data)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
