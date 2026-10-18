package messagehook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/pkg/util"
)

const maxVoiceDownloadSize = 32 << 20

func (s *Service) deliverPostWithKey(url string, evt Event, eventID string) DeliveryResult {
	return s.deliverPostWithKeyContext(context.Background(), url, evt, eventID)
}

func (s *Service) deliverPostWithKeyContext(ctx context.Context, url string, evt Event, eventID string) DeliveryResult {
	body, err := json.Marshal(evt)
	if err != nil {
		return DeliveryResult{Target: "post", Status: "failed", Detail: err.Error(), Success: false}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return DeliveryResult{Target: "post", Status: "failed", Detail: err.Error(), Success: false}
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(eventID) != "" {
		req.Header.Set("Idempotency-Key", eventID)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("url", url).Msg("message hook post failed")
		return DeliveryResult{Target: "post", Status: "failed", Detail: err.Error(), Success: false}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DeliveryResult{Target: "post", Status: "failed", Detail: resp.Status, Success: false}
	}
	return DeliveryResult{Target: "post", Status: "sent", Detail: resp.Status, Success: true}
}

func postRetryDelay(attempts int) time.Duration {
	delay := postRetryBaseDelay
	for i := 0; i < attempts && delay < maxPostRetryDelay; i++ {
		delay *= 2
	}
	if delay > maxPostRetryDelay {
		return maxPostRetryDelay
	}
	return delay
}

func (s *Service) resolveTriggerMedia(trigger *model.Message) ([]string, []string, error) {
	return s.resolveTriggerMediaContext(context.Background(), trigger)
}

func (s *Service) resolveTriggerMediaContext(ctx context.Context, trigger *model.Message) ([]string, []string, error) {
	mediaType, _ := extractTriggerMediaRef(trigger)
	paths, cleanup, err := s.resolveTriggerMediaOnce(trigger)
	if err == nil || mediaType != "voice" {
		return paths, cleanup, err
	}
	// Voice blobs may land in VoiceInfo slightly after the text row is visible.
	// Retry briefly to avoid degrading to text-only forwarding.
	lastErr := err
	for i := 0; i < voiceResolveRetries; i++ {
		timer := time.NewTimer(voiceResolveInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, cleanup, ctx.Err()
		case <-timer.C:
		}
		paths, cleanup, err = s.resolveTriggerMediaOnce(trigger)
		if err == nil {
			return paths, cleanup, nil
		}
		lastErr = err
	}
	return nil, nil, lastErr
}

func (s *Service) resolveTriggerMediaOnce(trigger *model.Message) ([]string, []string, error) {
	mediaType, keys := extractTriggerMediaRef(trigger)
	if mediaType == "" || len(keys) == 0 {
		return nil, nil, nil
	}
	if mediaType == "voice" {
		// Voice forwarding requirement: always fetch via media_url first and
		// transcode to m4a before sending.
		for _, key := range keys {
			path, err := s.downloadTriggerMedia(mediaType, key)
			if err == nil && path != "" {
				return []string{path}, []string{path}, nil
			}
		}
		// Fallback when /voice/{key} is temporarily unavailable.
		for _, key := range keys {
			media, err := s.db.GetMedia(mediaType, key)
			if err != nil || media == nil || len(media.Data) == 0 {
				continue
			}
			path, err := s.writeVoiceAsM4A(media.Data, "")
			if err == nil && path != "" {
				return []string{path}, []string{path}, nil
			}
		}
		return nil, nil, fmt.Errorf("media file not found")
	}
	for _, key := range keys {
		path, err := s.downloadTriggerMedia(mediaType, key)
		if err == nil && path != "" {
			return []string{path}, []string{path}, nil
		}
	}
	for _, key := range keys {
		media, err := s.db.GetMedia(mediaType, key)
		if err != nil || media == nil {
			continue
		}
		path := strings.TrimSpace(media.Path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.conf.GetDataDir(), path)
		}
		if _, err := os.Stat(path); err == nil {
			return []string{path}, nil, nil
		}
	}
	return nil, nil, fmt.Errorf("media file not found")
}

func (s *Service) downloadTriggerMedia(mediaType, key string) (string, error) {
	url := s.buildTriggerMediaURL(mediaType, key)
	if url == "" {
		return "", fmt.Errorf("media url unavailable")
	}
	resp, err := s.httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("media download failed: status=%d", resp.StatusCode)
	}
	dir, err := s.ensureMediaDownloadDir()
	if err != nil {
		return "", err
	}
	if mediaType == "voice" {
		if resp.ContentLength > maxVoiceDownloadSize {
			return "", fmt.Errorf("voice media exceeds %d bytes", maxVoiceDownloadSize)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxVoiceDownloadSize+1))
		if err != nil {
			return "", err
		}
		if len(raw) > maxVoiceDownloadSize {
			return "", fmt.Errorf("voice media exceeds %d bytes", maxVoiceDownloadSize)
		}
		return s.writeVoiceAsM4A(raw, resp.Header.Get("Content-Type"))
	}
	pattern := "chatlog-weixin-media-*"
	if name := filenameFromResponse(resp); name != "" {
		if filepath.Ext(name) == "" {
			if ext := extensionFromContentType(resp.Header.Get("Content-Type")); ext != "" {
				name += ext
			} else {
				name += mediaSuffix(mediaType)
			}
		}
		pattern = "chatlog-weixin-media-*-" + name
	} else if ext := extensionFromContentType(resp.Header.Get("Content-Type")); ext != "" {
		pattern += ext
	} else {
		pattern += mediaSuffix(mediaType)
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if _, err := tmp.ReadFrom(resp.Body); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func (s *Service) ensureMediaDownloadDir() (string, error) {
	base := strings.TrimSpace(s.conf.GetWorkDir())
	if base == "" {
		account := strings.TrimSpace(s.conf.GetAccount())
		if account == "" {
			account = "server"
		}
		base = util.WorkDirAt(s.conf.GetRuntimeDir(), account)
	}
	dir := filepath.Join(base, "cache", "weixin_media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func filenameFromResponse(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	if cd := strings.TrimSpace(resp.Header.Get("Content-Disposition")); cd != "" {
		_, params, err := mime.ParseMediaType(cd)
		if err == nil {
			if name := sanitizeFilename(params["filename*"]); name != "" {
				return name
			}
			if name := sanitizeFilename(params["filename"]); name != "" {
				return name
			}
		}
	}
	if resp.Request != nil && resp.Request.URL != nil {
		escapedPath := strings.TrimSpace(resp.Request.URL.EscapedPath())
		if escapedPath == "" {
			escapedPath = strings.TrimSpace(resp.Request.URL.Path)
		}
		if escapedPath != "" {
			base := path.Base(escapedPath)
			if name, err := neturl.PathUnescape(base); err == nil {
				if name = sanitizeFilename(name); name != "" {
					return name
				}
			}
			if name := sanitizeFilename(base); name != "" {
				return name
			}
		}
	}
	return ""
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// RFC 5987 style: UTF-8''encoded_name
	if idx := strings.Index(name, "''"); idx >= 0 {
		name = name[idx+2:]
	}
	name = filepath.Base(filepath.Clean(name))
	name = strings.ReplaceAll(name, string(os.PathSeparator), "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "*", "_")
	if name == "." || name == ".." {
		return ""
	}
	return name
}

func (s *Service) writeVoiceAsM4A(data []byte, contentType string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("voice data empty")
	}
	dir, err := s.ensureMediaDownloadDir()
	if err != nil {
		return "", err
	}
	srcExt := extensionFromContentType(contentType)
	if srcExt == "" {
		srcExt = voiceExtForData(data)
	}
	src, err := os.CreateTemp(dir, "chatlog-weixin-voice-src-*"+srcExt)
	if err != nil {
		return "", err
	}
	if _, err := src.Write(data); err != nil {
		src.Close()
		_ = os.Remove(src.Name())
		return "", err
	}
	if err := src.Close(); err != nil {
		_ = os.Remove(src.Name())
		return "", err
	}
	dst, err := s.transcodeToM4A(src.Name())
	_ = os.Remove(src.Name())
	if err != nil {
		return "", err
	}
	return dst, nil
}

func (s *Service) transcodeToM4A(srcPath string) (string, error) {
	dir := filepath.Dir(srcPath)
	tmp, err := os.CreateTemp(dir, "chatlog-weixin-voice-*.m4a")
	if err != nil {
		return "", err
	}
	dstPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(dstPath)

	var lastErr error
	if ffmpeg, err := exec.LookPath("ffmpeg"); err == nil {
		cmd := exec.Command(ffmpeg, "-y", "-loglevel", "error", "-i", srcPath, "-vn", "-c:a", "aac", "-b:a", "64k", dstPath)
		if out, err := cmd.CombinedOutput(); err == nil {
			return dstPath, nil
		} else {
			lastErr = fmt.Errorf("ffmpeg transcode failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if afconvert, err := exec.LookPath("afconvert"); err == nil {
		cmd := exec.Command(afconvert, "-f", "m4af", "-d", "aac", srcPath, dstPath)
		if out, err := cmd.CombinedOutput(); err == nil {
			return dstPath, nil
		} else {
			lastErr = fmt.Errorf("afconvert transcode failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	_ = os.Remove(dstPath)
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no audio transcoder available (need ffmpeg or afconvert)")
}

func extensionFromContentType(contentType string) string {
	ct := strings.TrimSpace(contentType)
	if ct == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mediaType = ct
	}
	exts, _ := mime.ExtensionsByType(mediaType)
	for _, ext := range exts {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext != "" && strings.HasPrefix(ext, ".") {
			return ext
		}
	}
	return ""
}

func (s *Service) buildTriggerMediaURL(mediaType, key string) string {
	addr := strings.TrimSpace(s.conf.GetHTTPAddr())
	if addr == "" || strings.TrimSpace(key) == "" {
		return ""
	}
	host := addr
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "0.0.0.0:")
	} else if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	return "http://" + host + "/" + strings.TrimSpace(mediaType) + "/" + strings.TrimSpace(key)
}

func extractTriggerMediaRef(m *model.Message) (string, []string) {
	if m == nil || m.Contents == nil {
		return "", nil
	}
	get := func(key string) string {
		if v, ok := m.Contents[key]; ok {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}
	appendUnique := func(list []string, v string) []string {
		v = strings.TrimSpace(v)
		if v == "" {
			return list
		}
		for _, item := range list {
			if item == v {
				return list
			}
		}
		return append(list, v)
	}

	keys := make([]string, 0, 3)
	switch m.Type {
	case model.MessageTypeImage:
		keys = appendUnique(keys, get("path"))
		keys = appendUnique(keys, get("md5"))
		return "image", keys
	case model.MessageTypeVideo:
		keys = appendUnique(keys, get("path"))
		keys = appendUnique(keys, get("md5"))
		keys = appendUnique(keys, get("rawmd5"))
		return "video", keys
	case model.MessageTypeVoice:
		keys = appendUnique(keys, get("voice"))
		keys = appendUnique(keys, get("voice_local_id"))
		if m.Seq > 0 {
			keys = appendUnique(keys, fmt.Sprintf("%d", m.Seq%1000000))
		}
		return "voice", keys
	case model.MessageTypeShare:
		if m.SubType == model.MessageSubTypeFile {
			keys = appendUnique(keys, get("md5"))
			keys = appendUnique(keys, get("path"))
			return "file", keys
		}
	}
	return "", nil
}

func voiceExtForData(data []byte) string {
	if len(data) >= 4 && string(data[:4]) == "#!AM" {
		return ".amr"
	}
	return ".silk"
}

func mediaSuffix(mediaType string) string {
	switch mediaType {
	case "image":
		return ".jpg"
	case "video":
		return ".mp4"
	case "voice":
		return ".silk"
	case "file":
		return ".bin"
	default:
		return ".dat"
	}
}
