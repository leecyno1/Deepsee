package http

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/pkg/util"
)

const (
	snsVideoDecryptWindow          = 128 * 1024
	maxSNSVideoDecryptWindow       = 4 << 20
	maxSNSMediaResponseSize        = 64 << 20
	maxSNSMediaStreamSize    int64 = 512 << 20
	maxConcurrentSNSMedia          = 2
)

func (s *Service) handleSNSMediaProxy(c *gin.Context) {
	rawURL := strings.TrimSpace(c.Query("url"))
	if rawURL == "" {
		errors.Err(c, errors.InvalidArg("url"))
		return
	}
	key := strings.TrimSpace(c.Query("key"))
	if key == "" || key == "0" {
		key = s.getSNSMediaKeyFromCache(rawURL)
	}

	cachePath := s.snsMediaCachePath(rawURL, key)
	if served, err := serveCachedSNSMedia(c, cachePath); served {
		return
	} else if err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("path", cachePath).Msg("read sns media cache failed")
	}

	release, err := s.acquireSNSMediaSlot(c.Request.Context())
	if err != nil {
		errors.Err(c, err)
		return
	}
	defer release()

	if err := validatePublicHTTPURL(rawURL); err != nil {
		errors.Err(c, errors.InvalidArg("url"))
		return
	}
	requestRange := strings.TrimSpace(c.GetHeader("Range"))
	if !validSNSByteRange(requestRange) {
		errors.Err(c, errors.New(nil, http.StatusRequestedRangeNotSatisfiable, "invalid media byte range"))
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		errors.Err(c, errors.InvalidArg("url"))
		return
	}
	req.Header.Set("User-Agent", "MicroMessenger Client")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")
	if requestRange != "" {
		req.Header.Set("Range", requestRange)
	}

	resp, err := s.publicHTTPClient().Do(req)
	if err != nil {
		errors.Err(c, errors.OpenFileFailed(rawURL, err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		errors.Err(c, errors.New(nil, http.StatusRequestedRangeNotSatisfiable, "media byte range is not satisfiable"))
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		errors.Err(c, errors.New(nil, http.StatusBadGateway, fmt.Sprintf("sns media upstream status %d", resp.StatusCode)))
		return
	}
	if err := validateSNSMediaStreamSize(resp); err != nil {
		errors.Err(c, err)
		return
	}

	if isSNSVideoResponse(resp) {
		if err := s.streamSNSVideoResponse(c, resp, key, requestRange, cachePath); err != nil {
			if !c.Writer.Written() {
				errors.Err(c, err)
			} else {
				log.Warn().Err(err).Str("url", rawURL).Msg("stream sns video failed")
			}
		}
		return
	}

	if resp.ContentLength > maxSNSMediaResponseSize {
		errors.Err(c, errors.New(nil, http.StatusRequestEntityTooLarge, "sns image response is too large"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSNSMediaResponseSize+1))
	if err != nil {
		errors.Err(c, errors.ReadFileFailed(rawURL, err))
		return
	}
	if len(data) == 0 {
		errors.Err(c, errors.New(nil, http.StatusBadGateway, "empty sns media response"))
		return
	}
	if len(data) > maxSNSMediaResponseSize {
		errors.Err(c, errors.New(nil, http.StatusRequestEntityTooLarge, "sns image response is too large"))
		return
	}
	data, contentType, err := s.decryptSNSMedia(c.Request.Context(), data, key, resp.Header.Get("Content-Type"))
	if err != nil {
		errors.Err(c, err)
		return
	}
	if cachePath != "" {
		if writeErr := util.WritePrivateFileAtomic(cachePath, data); writeErr != nil {
			log.Warn().Err(writeErr).Str("path", cachePath).Msg("write sns media cache failed")
		}
	}
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, contentType, data)
}

func serveCachedSNSMedia(c *gin.Context, cachePath string) (bool, error) {
	if strings.TrimSpace(cachePath) == "" {
		return false, nil
	}
	file, err := os.Open(cachePath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return false, fmt.Errorf("invalid sns media cache file")
	}
	head := make([]byte, minIntLocal(int(info.Size()), 512))
	if _, err := file.ReadAt(head, 0); err != nil && err != io.EOF {
		return false, err
	}
	contentType := detectSNSMime(head, "")
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=86400")
	http.ServeContent(c.Writer, c.Request, filepath.Base(cachePath), info.ModTime(), file)
	return true, nil
}

func validSNSByteRange(value string) bool {
	if value == "" {
		return true
	}
	if !strings.HasPrefix(value, "bytes=") {
		return false
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, "bytes="))
	if value == "" || strings.Contains(value, ",") {
		return false
	}
	parts := strings.Split(value, "-")
	if len(parts) != 2 || (parts[0] == "" && parts[1] == "") {
		return false
	}
	var start, end uint64
	var err error
	if parts[0] != "" {
		start, err = strconv.ParseUint(parts[0], 10, 63)
		if err != nil {
			return false
		}
	}
	if parts[1] != "" {
		end, err = strconv.ParseUint(parts[1], 10, 63)
		if err != nil {
			return false
		}
	}
	return parts[0] == "" || parts[1] == "" || start <= end
}

func validateSNSMediaStreamSize(resp *http.Response) error {
	_, _, total, hasRange := parseSNSContentRange(resp.Header.Get("Content-Range"))
	if hasRange && total > maxSNSMediaStreamSize {
		return errors.New(nil, http.StatusRequestEntityTooLarge, "sns video response is too large")
	}
	if !hasRange && resp.ContentLength > maxSNSMediaStreamSize {
		return errors.New(nil, http.StatusRequestEntityTooLarge, "sns video response is too large")
	}
	return nil
}

func parseSNSContentRange(value string) (start, end, total int64, ok bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "bytes ") {
		return 0, 0, 0, false
	}
	rangeAndTotal := strings.SplitN(strings.TrimSpace(value[6:]), "/", 2)
	if len(rangeAndTotal) != 2 || rangeAndTotal[1] == "*" {
		return 0, 0, 0, false
	}
	bounds := strings.SplitN(rangeAndTotal[0], "-", 2)
	if len(bounds) != 2 {
		return 0, 0, 0, false
	}
	start, errStart := strconv.ParseInt(bounds[0], 10, 64)
	end, errEnd := strconv.ParseInt(bounds[1], 10, 64)
	total, errTotal := strconv.ParseInt(rangeAndTotal[1], 10, 64)
	if errStart != nil || errEnd != nil || errTotal != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func isSNSVideoResponse(resp *http.Response) bool {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(contentType, "video/") ||
		strings.TrimSpace(resp.Header.Get("X-snsvideoflag")) != "" ||
		strings.TrimSpace(resp.Header.Get("X-enclen")) != ""
}

func snsVideoEncryptionWindow(resp *http.Response) int64 {
	if strings.TrimSpace(resp.Header.Get("X-encflag")) == "0" {
		return 0
	}
	rawWindow := strings.TrimSpace(resp.Header.Get("X-enclen"))
	if rawWindow == "" {
		return snsVideoDecryptWindow
	}
	window, err := strconv.ParseInt(rawWindow, 10, 64)
	if err != nil || window < 0 {
		return snsVideoDecryptWindow
	}
	if window > maxSNSVideoDecryptWindow {
		window = maxSNSVideoDecryptWindow
	}
	return window
}

func (s *Service) streamSNSVideoResponse(c *gin.Context, resp *http.Response, key, requestRange, cachePath string) error {
	start, end, total, hasRange := parseSNSContentRange(resp.Header.Get("Content-Range"))
	if !hasRange {
		start = 0
		if resp.ContentLength > 0 {
			end = resp.ContentLength - 1
			total = resp.ContentLength
		}
	}

	window := snsVideoEncryptionWindow(resp)
	prefixSize := window
	if resp.ContentLength >= 0 && prefixSize > resp.ContentLength {
		prefixSize = resp.ContentLength
	}
	prefix, err := io.ReadAll(io.LimitReader(resp.Body, prefixSize))
	if err != nil {
		return errors.ReadFileFailed("sns video prefix", err)
	}
	if len(prefix) == 0 && resp.ContentLength == 0 {
		return errors.New(nil, http.StatusBadGateway, "empty sns video response")
	}

	contentType := detectSNSMime(prefix, resp.Header.Get("Content-Type"))
	if start == 0 && strings.HasPrefix(detectSNSMimeStrict(prefix), "video/") {
		window = 0
	} else if len(prefix) > 0 && window > 0 {
		prefix, err = s.decryptSNSVideoSegment(c.Request.Context(), prefix, key, window)
		if err != nil {
			return err
		}
		if start == 0 {
			contentType = detectSNSMime(prefix, resp.Header.Get("Content-Type"))
			if !strings.HasPrefix(contentType, "video/") {
				return errors.QueryFailed("sns video decrypt failed", nil)
			}
		}
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "video/") {
		contentType = "video/mp4"
	}

	copySNSMediaResponseHeaders(c, resp, contentType)

	cacheFile, cacheTempPath, cacheEnabled, err := prepareSNSStreamCache(
		cachePath,
		requestRange,
		resp.StatusCode,
		start,
		end,
		total,
		resp.ContentLength,
	)
	if err != nil {
		log.Warn().Err(err).Str("path", cachePath).Msg("prepare sns media cache failed")
	}
	if cacheFile != nil {
		defer func() {
			if cacheFile != nil {
				_ = cacheFile.Close()
			}
			if cacheTempPath != "" {
				_ = os.Remove(cacheTempPath)
			}
		}()
	}

	c.Status(resp.StatusCode)
	var output io.Writer = c.Writer
	if cacheEnabled {
		output = io.MultiWriter(c.Writer, cacheFile)
	}
	written, err := output.Write(prefix)
	if err != nil {
		return err
	}
	totalWritten := int64(written)

	remainingLimit := maxSNSMediaStreamSize - totalWritten
	if resp.ContentLength >= 0 {
		remainingLimit = resp.ContentLength - totalWritten
	}
	if remainingLimit < 0 {
		return errors.New(nil, http.StatusRequestEntityTooLarge, "sns video response is too large")
	}
	copied, err := io.Copy(output, io.LimitReader(resp.Body, remainingLimit+1))
	totalWritten += copied
	if err != nil {
		return errors.ReadFileFailed("sns video response", err)
	}
	if copied > remainingLimit {
		return errors.New(nil, http.StatusRequestEntityTooLarge, "sns video response is too large")
	}
	if resp.ContentLength >= 0 && totalWritten != resp.ContentLength {
		return errors.ReadFileFailed("sns video response", io.ErrUnexpectedEOF)
	}

	if cacheEnabled {
		if err := finalizeSNSStreamCache(cacheFile, cacheTempPath, cachePath); err != nil {
			log.Warn().Err(err).Str("path", cachePath).Msg("finalize sns media cache failed")
		} else {
			cacheFile = nil
			cacheTempPath = ""
		}
	}
	return nil
}

func (s *Service) decryptSNSVideoSegment(ctx context.Context, data []byte, key string, window int64) ([]byte, error) {
	if len(data) == 0 || window <= 0 {
		return data, nil
	}
	key = strings.TrimSpace(key)
	if key == "" || key == "0" {
		return nil, errors.QueryFailed("sns media decrypt key missing", nil)
	}
	seed, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return nil, errors.QueryFailed("invalid sns media decrypt key", err)
	}
	if window <= 0 || window > maxSNSVideoDecryptWindow {
		return nil, errors.QueryFailed("invalid sns video decrypt window", nil)
	}
	stream, streamErr := s.getKeystreamPreferWASM(ctx, key, int(window), true)
	if streamErr != nil {
		stream = make([]byte, int(window))
		newISAAC64(seed).fillKeystream(stream)
		reverseBytes(stream)
	}
	decoded := append([]byte(nil), data...)
	overlap := minIntLocal(len(decoded), int(window))
	for index := 0; index < overlap; index++ {
		decoded[index] ^= stream[index]
	}
	return decoded, nil
}

func copySNSMediaResponseHeaders(c *gin.Context, resp *http.Response, contentType string) {
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("Accept-Ranges", "bytes")
	for _, name := range []string{"Content-Length", "Content-Range", "ETag", "Last-Modified"} {
		if value := strings.TrimSpace(resp.Header.Get(name)); value != "" {
			c.Header(name, value)
		}
	}
}

func prepareSNSStreamCache(
	cachePath, requestRange string,
	status int,
	start, end, total, contentLength int64,
) (*os.File, string, bool, error) {
	if strings.TrimSpace(cachePath) == "" {
		return nil, "", false, nil
	}
	fullResponse := requestRange == "" && status == http.StatusOK
	if total > 0 {
		fullResponse = start == 0 && end == total-1 && contentLength == total
	}
	if !fullResponse {
		return nil, "", false, nil
	}
	cacheDir := filepath.Dir(cachePath)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, "", false, err
	}
	if err := os.Chmod(cacheDir, 0o700); err != nil {
		return nil, "", false, err
	}
	file, err := os.CreateTemp(cacheDir, "."+filepath.Base(cachePath)+".tmp-*")
	if err != nil {
		return nil, "", false, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, "", false, err
	}
	return file, file.Name(), true, nil
}

func finalizeSNSStreamCache(file *os.File, tempPath, cachePath string) error {
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		return err
	}
	return os.Chmod(cachePath, 0o600)
}

func (s *Service) downloadAndDecryptSNSMedia(ctx context.Context, rawURL, key string) ([]byte, string, error) {
	release, err := s.acquireSNSMediaSlot(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()

	if err := validatePublicHTTPURL(rawURL); err != nil {
		return nil, "", errors.InvalidArg("url")
	}
	cachePath := s.snsMediaCachePath(rawURL, key)
	if cachePath != "" {
		if data, err := readLocalMediaFile(cachePath); err == nil && len(data) > 0 {
			return data, detectSNSMime(data, ""), nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", errors.InvalidArg("url")
	}
	req.Header.Set("User-Agent", "MicroMessenger Client")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")

	client := s.publicHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", errors.OpenFileFailed(rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, "", errors.QueryFailed(fmt.Sprintf("sns media http status %d", resp.StatusCode), nil)
	}
	if resp.ContentLength > maxSNSMediaResponseSize {
		return nil, "", errors.QueryFailed("sns media response is too large", nil)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSNSMediaResponseSize+1))
	if err != nil {
		return nil, "", errors.ReadFileFailed(rawURL, err)
	}
	if len(data) == 0 {
		return nil, "", errors.QueryFailed("empty sns media response", nil)
	}
	if len(data) > maxSNSMediaResponseSize {
		return nil, "", errors.QueryFailed("sns media response is too large", nil)
	}

	data, contentType, err := s.decryptSNSMedia(ctx, data, key, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}

	if cachePath != "" {
		cacheDir := filepath.Dir(cachePath)
		if err := os.MkdirAll(cacheDir, 0o700); err == nil {
			_ = os.Chmod(cacheDir, 0o700)
			if writeErr := util.WritePrivateFileAtomic(cachePath, data); writeErr != nil {
				log.Warn().Err(writeErr).Str("path", cachePath).Msg("write sns media cache failed")
			}
		}
	}
	return data, contentType, nil
}

func (s *Service) acquireSNSMediaSlot(ctx context.Context) (func(), error) {
	s.snsMediaSlotOnce.Do(func() {
		s.snsMediaSlots = make(chan struct{}, maxConcurrentSNSMedia)
	})
	select {
	case s.snsMediaSlots <- struct{}{}:
		return func() { <-s.snsMediaSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) publicHTTPClient() *http.Client {
	s.snsHTTPClientMu.Lock()
	defer s.snsHTTPClientMu.Unlock()
	if s.snsHTTPClient == nil {
		s.snsHTTPClient = newPublicHTTPClient()
	}
	return s.snsHTTPClient
}

func validatePublicHTTPURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("invalid public HTTP URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !isPublicOutboundIP(ip) {
		return fmt.Errorf("private or local destination is not allowed")
	}
	return nil
}

func isPublicOutboundIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

func newPublicHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid outbound address: %w", err)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, candidate := range ips {
				if !isPublicOutboundIP(candidate.IP) {
					return nil, fmt.Errorf("private or local destination is not allowed")
				}
			}
			for _, candidate := range ips {
				if isPublicOutboundIP(candidate.IP) {
					return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
				}
			}
			return nil, fmt.Errorf("destination has no public IP address")
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return validatePublicHTTPURL(req.URL.String())
		},
	}
}
