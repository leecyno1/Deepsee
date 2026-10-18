package http

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/pkg/util"
)

type runtimeCacheCategory struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind"`
	Entries     int64    `json:"entries"`
	Bytes       int64    `json:"bytes"`
	Capacity    int      `json:"capacity,omitempty"`
	Description string   `json:"description"`
	Clearable   bool     `json:"clearable"`
	Paths       []string `json:"paths,omitempty"`
}

type runtimeCacheFile struct {
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	ModifiedAt string `json:"modified_at"`
}

type cacheClearResult struct {
	Category     string `json:"category"`
	DeletedFiles int64  `json:"deleted_files"`
	FreedBytes   int64  `json:"freed_bytes"`
}

var runtimeCacheCategoryOrder = []string{
	"media_paths",
	"sns_keys",
	"statistics",
	"database_audit",
	"decoded_media",
	"sns_media",
	"sns_wasm",
	"wcdb",
	"push_media",
}

func (s *Service) initCacheRoutes() {
	s.router.GET("/api/v1/cache", s.handleCacheStatus)
	s.router.GET("/api/v1/cache/files", s.handleCacheFiles)
	s.router.POST("/api/v1/cache/clear", s.handleClearCache)
	s.router.POST("/api/v1/cache/open", s.handleOpenCachePath)
}

func (s *Service) handleCacheStatus(c *gin.Context) {
	categories, err := s.runtimeCacheCategories()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var totalBytes int64
	var totalEntries int64
	for _, category := range categories {
		totalBytes += category.Bytes
		totalEntries += category.Entries
	}
	c.JSON(http.StatusOK, gin.H{
		"categories":    categories,
		"total_bytes":   totalBytes,
		"total_entries": totalEntries,
		"timestamp":     time.Now().Format(time.RFC3339Nano),
	})
}

func (s *Service) handleClearCache(c *gin.Context) {
	category := strings.ToLower(strings.TrimSpace(c.DefaultQuery("category", "all")))
	if category == "" {
		category = "all"
	}
	valid := category == "all"
	for _, candidate := range runtimeCacheCategoryOrder {
		if category == candidate {
			valid = true
			break
		}
	}
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cache category"})
		return
	}

	targets := []string{category}
	if category == "all" {
		targets = append([]string(nil), runtimeCacheCategoryOrder...)
	}
	results := make([]cacheClearResult, 0, len(targets))
	for _, target := range targets {
		result, err := s.clearRuntimeCacheCategory(target)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "category": target})
			return
		}
		results = append(results, result)
	}
	log.Info().Str("category", category).Interface("results", results).Msg("Cleared runtime cache")
	c.JSON(http.StatusOK, gin.H{
		"message":  "Cache cleared successfully",
		"category": category,
		"results":  results,
	})
}

func (s *Service) handleCacheFiles(c *gin.Context) {
	category := strings.ToLower(strings.TrimSpace(c.Query("category")))
	if !s.validDiskCacheCategory(category) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid disk cache category"})
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 500, 1, 5000)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
		return
	}
	offset, err := parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 100_000)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
		return
	}
	files, err := s.runtimeCacheFiles(category)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	total := len(files)
	if offset >= len(files) {
		files = []runtimeCacheFile{}
	} else {
		files = files[offset:]
		if len(files) > limit {
			files = files[:limit]
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"category": category,
		"roots":    s.runtimeCachePaths(category),
		"files":    files,
		"count":    len(files),
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(files) < total,
	})
}

var openRuntimeCachePath = func(path string) error {
	return exec.Command("open", path).Run()
}

func (s *Service) handleOpenCachePath(c *gin.Context) {
	category := strings.ToLower(strings.TrimSpace(c.Query("category")))
	if !s.validDiskCacheCategory(category) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid disk cache category"})
		return
	}
	roots := s.runtimeCachePaths(category)
	path := strings.TrimSpace(c.Query("path"))
	if path == "" && len(roots) > 0 {
		path = roots[0]
	}
	if path == "" || !pathWithinRoots(path, roots) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cache path"})
		return
	}
	path, err := filepath.Abs(path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cache path"})
		return
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "cache path does not exist"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := openRuntimeCachePath(path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "category": category, "path": path})
}

func (s *Service) runtimeCacheCategories() ([]runtimeCacheCategory, error) {
	decodedFiles, decodedBytes, decodedErr := scanDecodedMediaCache(s.conf.GetDataDir(), false)
	if decodedErr != nil {
		return nil, fmt.Errorf("scan decoded media cache: %w", decodedErr)
	}
	snsFiles, snsBytes, err := scanDirectoryCache(s.snsCacheDir())
	if err != nil {
		return nil, fmt.Errorf("scan sns media cache: %w", err)
	}
	wasmFiles, wasmBytes, err := scanDirectoryCache(s.snsWasmCacheDir())
	if err != nil {
		return nil, fmt.Errorf("scan sns wasm cache: %w", err)
	}
	wcdbFiles, wcdbBytes, err := scanDirectoryCache(util.DefaultCacheDir("wcdb"))
	if err != nil {
		return nil, fmt.Errorf("scan wcdb cache: %w", err)
	}
	pushFiles, pushBytes, err := scanDirectoryCache(s.pushMediaCacheDir())
	if err != nil {
		return nil, fmt.Errorf("scan push media cache: %w", err)
	}

	return []runtimeCacheCategory{
		{ID: "media_paths", Label: "媒体路径索引", Kind: "memory", Entries: int64(s.md5PathCache.Len()), Capacity: md5PathCacheLimit, Description: "媒体 MD5 到本地路径的内存索引", Clearable: true},
		{ID: "sns_keys", Label: "朋友圈密钥索引", Kind: "memory", Entries: int64(s.snsMediaKeyCache.Len()), Capacity: snsMediaKeyCacheLimit, Description: "朋友圈媒体 URL 对应密钥的内存索引", Clearable: true},
		{ID: "statistics", Label: "聊天统计结果", Kind: "memory", Entries: int64(s.statsCache.Len()), Capacity: statsCacheLimit, Description: "聊天分析接口的短期统计缓存", Clearable: true},
		{ID: "database_audit", Label: "数据库解析审计", Kind: "memory", Entries: s.databaseAuditCacheEntries(), Capacity: 1, Description: "全库解析覆盖率和分片水位的五分钟审计快照", Clearable: true},
		{ID: "decoded_media", Label: "已解码媒体文件", Kind: "disk", Entries: decodedFiles, Bytes: decodedBytes, Description: "数据目录中由 DAT 文件生成的图片和视频", Clearable: true, Paths: s.runtimeCachePaths("decoded_media")},
		{ID: "sns_media", Label: "朋友圈媒体文件", Kind: "disk", Entries: snsFiles, Bytes: snsBytes, Description: "已下载并解密的朋友圈图片和视频", Clearable: true, Paths: s.runtimeCachePaths("sns_media")},
		{ID: "sns_wasm", Label: "朋友圈解码组件", Kind: "disk", Entries: wasmFiles, Bytes: wasmBytes, Description: "朋友圈视频解码所需的本地 WASM 资源", Clearable: true, Paths: s.runtimeCachePaths("sns_wasm")},
		{ID: "wcdb", Label: "旧版 WCDB 解密缓存", Kind: "disk", Entries: wcdbFiles, Bytes: wcdbBytes, Description: "旧版本遗留的完整解密副本；当前直读链路不使用", Clearable: true, Paths: s.runtimeCachePaths("wcdb")},
		{ID: "push_media", Label: "推送媒体文件", Kind: "disk", Entries: pushFiles, Bytes: pushBytes, Description: "消息推送流程临时下载的媒体文件", Clearable: true, Paths: s.runtimeCachePaths("push_media")},
	}, nil
}

func (s *Service) validDiskCacheCategory(category string) bool {
	switch category {
	case "decoded_media", "sns_media", "sns_wasm", "wcdb", "push_media":
		return true
	default:
		return false
	}
}

func (s *Service) runtimeCachePaths(category string) []string {
	var path string
	switch category {
	case "decoded_media":
		path = strings.TrimSpace(s.conf.GetDataDir())
	case "sns_media":
		path = s.snsCacheDir()
	case "sns_wasm":
		path = s.snsWasmCacheDir()
	case "wcdb":
		path = util.DefaultCacheDir("wcdb")
	case "push_media":
		path = s.pushMediaCacheDir()
	}
	if strings.TrimSpace(path) == "" {
		return nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return []string{filepath.Clean(path)}
	}
	return []string{filepath.Clean(absolute)}
}

func (s *Service) runtimeCacheFiles(category string) ([]runtimeCacheFile, error) {
	roots := s.runtimeCachePaths(category)
	files := make([]runtimeCacheFile, 0)
	for _, root := range roots {
		_, statErr := os.Stat(root)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}
			if category == "decoded_media" && !isDecodedMediaCacheFile(path) {
				return nil
			}
			files = append(files, runtimeCacheFile{
				Path:       path,
				Bytes:      info.Size(),
				ModifiedAt: info.ModTime().Format(time.RFC3339),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func pathWithinRoots(path string, roots []string) bool {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absolutePath = filepath.Clean(absolutePath)
	for _, root := range roots {
		absoluteRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(filepath.Clean(absoluteRoot), absolutePath)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (s *Service) clearRuntimeCacheCategory(category string) (cacheClearResult, error) {
	result := cacheClearResult{Category: category}
	switch category {
	case "media_paths":
		result.DeletedFiles = int64(s.md5PathCache.Len())
		s.md5PathCache.Clear()
	case "sns_keys":
		result.DeletedFiles = int64(s.snsMediaKeyCache.Len())
		s.snsMediaKeyCache.Clear()
	case "statistics":
		result.DeletedFiles = int64(s.statsCache.Len())
		s.statsCache.Clear()
	case "database_audit":
		result.DeletedFiles = s.databaseAuditCacheEntries()
		s.databaseAuditMu.Lock()
		s.databaseAudit = nil
		s.databaseAuditShards = nil
		s.databaseAuditMu.Unlock()
	case "decoded_media":
		files, bytes, err := scanDecodedMediaCache(s.conf.GetDataDir(), true)
		result.DeletedFiles, result.FreedBytes = files, bytes
		return result, err
	case "sns_media":
		files, bytes, err := clearDirectoryCache(s.snsCacheDir())
		result.DeletedFiles, result.FreedBytes = files, bytes
		return result, err
	case "sns_wasm":
		files, bytes, err := clearDirectoryCache(s.snsWasmCacheDir())
		if err == nil {
			snsWasmInitMu.Lock()
			snsWasmBaseDir = ""
			snsWasmInitMu.Unlock()
		}
		result.DeletedFiles, result.FreedBytes = files, bytes
		return result, err
	case "wcdb":
		files, bytes, err := s.db.ClearDecryptedCache()
		result.DeletedFiles, result.FreedBytes = files, bytes
		return result, err
	case "push_media":
		files, bytes, err := clearDirectoryCache(s.pushMediaCacheDir())
		result.DeletedFiles, result.FreedBytes = files, bytes
		return result, err
	default:
		return result, fmt.Errorf("unknown cache category %q", category)
	}
	return result, nil
}

func (s *Service) snsCacheDir() string {
	base := strings.TrimSpace(s.conf.GetWorkDir())
	if base == "" {
		base = util.WorkDirAt(s.conf.GetRuntimeDir(), "server")
	}
	return filepath.Join(base, "cache", "sns")
}

func (s *Service) snsWasmCacheDir() string {
	return util.CacheDirAt(s.conf.GetRuntimeDir(), "sns_wasm")
}

func (s *Service) pushMediaCacheDir() string {
	base := strings.TrimSpace(s.conf.GetWorkDir())
	if base == "" {
		base = util.WorkDirAt(s.conf.GetRuntimeDir(), "server")
	}
	return filepath.Join(base, "cache", "weixin_media")
}

func scanDecodedMediaCache(root string, remove bool) (int64, int64, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, 0, nil
	}
	var files int64
	var bytes int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !info.Mode().IsRegular() || !isDecodedMediaCacheFile(path) {
			return nil
		}
		if remove {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes, err
}

func isDecodedMediaCacheFile(path string) bool {
	if !isGeneratedMediaExtension(filepath.Ext(path)) {
		return false
	}
	baseName := strings.TrimSuffix(path, filepath.Ext(path))
	for _, suffix := range []string{"_h.dat", ".dat", "_t.dat"} {
		if sourceInfo, err := os.Stat(baseName + suffix); err == nil && sourceInfo.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func isGeneratedMediaExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".mp4":
		return true
	default:
		return false
	}
}

func scanDirectoryCache(root string) (int64, int64, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, 0, nil
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return 0, 0, nil
	} else if err != nil {
		return 0, 0, err
	}
	var files int64
	var bytes int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode().IsRegular() {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes, err
}

func clearDirectoryCache(root string) (int64, int64, error) {
	root = strings.TrimSpace(root)
	if root == "" || filepath.Clean(root) == string(filepath.Separator) {
		return 0, 0, fmt.Errorf("invalid cache directory")
	}
	files, bytes, err := scanDirectoryCache(root)
	if err != nil {
		return 0, 0, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return 0, 0, err
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return 0, 0, err
	}
	return files, bytes, nil
}
