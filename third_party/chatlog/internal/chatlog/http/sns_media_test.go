package http

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestValidatePublicHTTPURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://example.com/media.jpg", want: true},
		{url: "http://8.8.8.8/media.jpg", want: true},
		{url: "file:///etc/passwd", want: false},
		{url: "http://127.0.0.1/admin", want: false},
		{url: "http://169.254.169.254/latest/meta-data", want: false},
		{url: "http://10.0.0.1/internal", want: false},
		{url: "http://user:pass@example.com/media", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			got := validatePublicHTTPURL(tc.url) == nil
			if got != tc.want {
				t.Fatalf("allowed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGeneratedHTTPStateStaysInConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "workspaces", "wxid_example")
	cfg := pathTestConfig{lifecycleConfig: lifecycleConfig{}, root: root, workDir: workDir}
	s := &Service{conf: cfg}

	for _, path := range []string{s.snsMediaCachePath("https://example.com/image", "key")} {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("generated path escaped runtime root: root=%q path=%q rel=%q err=%v", root, path, rel, err)
		}
	}
}

func TestFixSNSArticleCoverURLKeepsThumbnailVariant(t *testing.T) {
	got := fixSNSArticleCoverURL("http://example.com/path/150", "cover token")
	want := "https://example.com/path/150?token=cover+token&idx=1"
	if got != want {
		t.Fatalf("article cover URL = %q, want %q", got, want)
	}
	if ordinary := fixSNSURL("http://example.com/path/150", "cover token", false); !strings.Contains(ordinary, "/0?") {
		t.Fatalf("ordinary timeline image was not upgraded: %q", ordinary)
	}
}

func TestISAAC64FillKeystreamMatchesAllocatedForm(t *testing.T) {
	for _, size := range []int{0, 1, 7, 8, 9, 127, 128, 129} {
		want := newISAAC64(42).generateKeystream(size)
		got := make([]byte, size)
		newISAAC64(42).fillKeystream(got)
		if string(got) != string(want) {
			t.Fatalf("size %d keystream mismatch", size)
		}
	}
}

func TestValidSNSByteRange(t *testing.T) {
	for _, value := range []string{"", "bytes=0-", "bytes=0-1023", "bytes=-512"} {
		if !validSNSByteRange(value) {
			t.Fatalf("expected valid range %q", value)
		}
	}
	for _, value := range []string{"items=0-1", "bytes=", "bytes=-", "bytes=9-3", "bytes=0-1,3-4"} {
		if validSNSByteRange(value) {
			t.Fatalf("expected invalid range %q", value)
		}
	}
}

func TestParseSNSContentRange(t *testing.T) {
	start, end, total, ok := parseSNSContentRange("bytes 1024-2047/4096")
	if !ok || start != 1024 || end != 2047 || total != 4096 {
		t.Fatalf("parsed range = (%d, %d, %d, %v)", start, end, total, ok)
	}
	if _, _, _, ok := parseSNSContentRange("bytes 0-4096/4096"); ok {
		t.Fatal("accepted range whose end exceeds total")
	}
}

func TestSNSVideoEncryptionWindowHonorsExplicitZero(t *testing.T) {
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set("X-encflag", "1")
	response.Header.Set("X-enclen", "0")
	if got := snsVideoEncryptionWindow(response); got != 0 {
		t.Fatalf("window = %d, want 0", got)
	}
	response.Header.Set("X-enclen", "65536")
	if got := snsVideoEncryptionWindow(response); got != 65536 {
		t.Fatalf("window = %d, want 65536", got)
	}
}

func TestStreamSNSVideoResponseDecryptsBoundedPrefix(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	cfg := pathTestConfig{lifecycleConfig: lifecycleConfig{}, root: root, workDir: filepath.Join(root, "work")}
	s := &Service{conf: cfg}

	const key = "1186350288"
	const size = snsVideoDecryptWindow + 4096
	plain := bytes.Repeat([]byte{0x5a}, size)
	copy(plain[:12], []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	encrypted := append([]byte(nil), plain...)
	stream := make([]byte, snsVideoDecryptWindow)
	seed, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	newISAAC64(seed).fillKeystream(stream)
	reverseBytes(stream)
	for index := range stream {
		encrypted[index] ^= stream[index]
	}

	response := &http.Response{
		StatusCode:    http.StatusPartialContent,
		Body:          io.NopCloser(bytes.NewReader(encrypted)),
		ContentLength: int64(len(encrypted)),
		Header:        make(http.Header),
	}
	response.Header.Set("Content-Type", "video/mp4")
	response.Header.Set("Content-Length", strconv.Itoa(len(encrypted)))
	response.Header.Set("Content-Range", "bytes 0-"+strconv.Itoa(len(encrypted)-1)+"/"+strconv.Itoa(len(encrypted)))
	response.Header.Set("X-encflag", "1")
	response.Header.Set("X-enclen", strconv.Itoa(snsVideoDecryptWindow))
	response.Header.Set("X-snsvideoflag", "xV0")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/sns/media/proxy", nil)
	cachePath := filepath.Join(root, "cache", "sns", "video.bin")
	if err := s.streamSNSVideoResponse(ctx, response, key, "bytes=0-", cachePath); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Range"); got != response.Header.Get("Content-Range") {
		t.Fatalf("content range = %q", got)
	}
	if !bytes.Equal(recorder.Body.Bytes(), plain) {
		t.Fatal("streamed video did not match decrypted content")
	}
	cached, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, plain) {
		t.Fatal("cached video did not match decrypted content")
	}
}

func TestServeCachedSNSMediaSupportsRange(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "video.bin")
	content := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 1, 2, 3, 4}
	if err := os.WriteFile(cachePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/sns/media/proxy", nil)
	ctx.Request.Header.Set("Range", "bytes=4-11")

	served, err := serveCachedSNSMedia(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !served {
		t.Fatal("cached media was not served")
	}
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != "ftypisom" {
		t.Fatalf("body = %q", got)
	}
}

func TestDecryptSNSVideoRangePrefix(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	s := &Service{conf: pathTestConfig{lifecycleConfig: lifecycleConfig{}, root: root, workDir: root}}
	const key = "42"
	const window = int64(8192)
	plain := bytes.Repeat([]byte{0x3c}, 8192)
	stream := make([]byte, window)
	newISAAC64(42).fillKeystream(stream)
	reverseBytes(stream)
	encrypted := append([]byte(nil), plain...)
	for index := range encrypted {
		encrypted[index] ^= stream[index]
	}
	got, err := s.decryptSNSVideoSegment(context.Background(), encrypted, key, window)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("range prefix decrypt mismatch")
	}
}

type pathTestConfig struct {
	lifecycleConfig
	root    string
	workDir string
}

func (c pathTestConfig) GetRuntimeDir() string { return c.root }
func (c pathTestConfig) GetWorkDir() string    { return c.workDir }
