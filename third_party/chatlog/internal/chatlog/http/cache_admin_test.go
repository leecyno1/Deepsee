package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDecodedMediaCacheScanAndClear(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "photo.dat")
	cached := filepath.Join(root, "photo.jpg")
	unrelated := filepath.Join(root, "keep.jpg")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("cached-image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("original-image"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, bytes, err := scanDecodedMediaCache(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || bytes != int64(len("cached-image")) {
		t.Fatalf("cache scan = %d files / %d bytes", files, bytes)
	}
	files, _, err = scanDecodedMediaCache(root, true)
	if err != nil || files != 1 {
		t.Fatalf("cache clear = %d files, err=%v", files, err)
	}
	if _, err := os.Stat(cached); !os.IsNotExist(err) {
		t.Fatalf("decoded cache file remains: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated image was removed: %v", err)
	}
}

func TestClearDirectoryCachePreservesRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "cache.bin"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, bytes, err := clearDirectoryCache(root)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || bytes != int64(len("cache")) {
		t.Fatalf("cleared = %d files / %d bytes", files, bytes)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("cache root was not preserved: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache root is not empty: entries=%v err=%v", entries, err)
	}
}

func TestClearCacheHandlerClearsSelectedMemoryCategory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{}
	service.md5PathCache.Set("media", "/tmp/media", md5PathCacheLimit)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/cache/clear?category=media_paths", nil)

	service.handleClearCache(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if service.md5PathCache.Len() != 0 {
		t.Fatal("selected memory cache remains populated")
	}
}

func TestClearCacheHandlerClearsDatabaseAuditSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{databaseAudit: &databaseAuditReport{GeneratedAt: "fixture"}}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/cache/clear?category=database_audit", nil)

	service.handleClearCache(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if service.databaseAuditCacheEntries() != 0 {
		t.Fatal("database audit cache remains populated")
	}
}

func TestCacheFilesListsConcretePathsAndOpenUsesSelectedPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	cacheDir := filepath.Join(workDir, "cache", "sns")
	cacheFile := filepath.Join(cacheDir, "nested", "preview.jpg")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{conf: pathTestConfig{root: root, workDir: workDir}}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/v1/cache/files?category=sns_media&limit=20", nil)
	service.handleCacheFiles(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var listing struct {
		Roots []string           `json:"roots"`
		Files []runtimeCacheFile `json:"files"`
		Total int                `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Total != 1 || len(listing.Files) != 1 || listing.Files[0].Path != cacheFile {
		t.Fatalf("cache listing = %#v", listing)
	}
	if len(listing.Roots) != 1 || listing.Roots[0] != cacheDir {
		t.Fatalf("cache roots = %#v, want %q", listing.Roots, cacheDir)
	}

	originalOpen := openRuntimeCachePath
	defer func() { openRuntimeCachePath = originalOpen }()
	var opened string
	openRuntimeCachePath = func(path string) error {
		opened = path
		return nil
	}
	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/cache/open?category=sns_media&path="+cacheFile,
		nil,
	)
	service.handleOpenCachePath(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("open status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if opened != cacheFile {
		t.Fatalf("opened path = %q, want %q", opened, cacheFile)
	}
}

func TestOpenCachePathRejectsPathOutsideCategory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	cacheDir := filepath.Join(workDir, "cache", "sns")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{conf: pathTestConfig{root: root, workDir: workDir}}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/cache/open?category=sns_media&path="+outside,
		nil,
	)

	service.handleOpenCachePath(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
