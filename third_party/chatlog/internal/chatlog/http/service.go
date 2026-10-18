package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/errors"
)

type Service struct {
	conf      Config
	db        Database
	imageKeys ImageKeyProvider
	media     ports.MediaCodec

	router             *gin.Engine
	server             *http.Server
	listen             net.Listener
	serverMu           sync.Mutex
	events             *sseHub
	eventMu            sync.Mutex
	eventCancel        context.CancelFunc
	eventDone          chan struct{}
	eventMessageCancel func()

	// md5 到 path 的缓存（用于图片、视频等媒体文件）
	md5PathCache boundedCache[string, string]

	// 朋友圈媒体 URL 到解密 key 的缓存
	snsMediaKeyCache boundedCache[string, string]
	snsHTTPClient    *http.Client
	snsHTTPClientMu  sync.Mutex
	snsMediaSlots    chan struct{}
	snsMediaSlotOnce sync.Once

	// 本地 DAT 媒体解码会同时保留输入和输出，限制并发避免峰值内存叠加。
	localMediaSlots    chan struct{}
	localMediaSlotOnce sync.Once

	// 失败时自动刷新图片密钥的节流控制
	imgKeyRefreshMu   sync.Mutex
	lastImgKeyRefresh time.Time

	statsCache     boundedCache[string, statsCacheEntry]
	statsComputeMu sync.Mutex

	// 数据库解析审计按需执行并短期缓存。计算锁避免重复全库扫描，
	// 读写锁让 2 秒一次的运行状态采样不被审计过程阻塞。
	databaseAuditRunMu  sync.Mutex
	databaseAuditMu     sync.RWMutex
	databaseAudit       *databaseAuditReport
	databaseAuditShards map[string]databaseAuditShardCache

	// 实时仪表盘只记录聚合值和最后一条请求摘要，避免保留请求体或查询参数。
	runtimeStartedAt       atomic.Int64
	runtimeRequestsTotal   atomic.Uint64
	runtimeRequestsActive  atomic.Int64
	runtimeRequestsPeak    atomic.Int64
	runtimeStatus2xx       atomic.Uint64
	runtimeStatus3xx       atomic.Uint64
	runtimeStatus4xx       atomic.Uint64
	runtimeStatus5xx       atomic.Uint64
	runtimeLatencyTotalNS  atomic.Uint64
	runtimeLatencyMaxNS    atomic.Uint64
	runtimeLastRequestAt   atomic.Int64
	runtimeLastRequest     atomic.Value
	runtimeDatabaseRead    atomic.Uint64
	runtimeDatabaseWritten atomic.Uint64
	runtimeDatabaseReads   atomic.Uint64
	runtimeDatabaseWrites  atomic.Uint64
	runtimeDatabaseIOMu    sync.Mutex
	runtimeDatabaseIOAt    time.Time
	runtimeDatabaseIORead  uint64
	runtimeDatabaseIOWrite uint64
	runtimeHTTPErrorsMu    sync.Mutex
	runtimeHTTPErrors      []runtimeHTTPErrorSnapshot
	runtimeHTTPErrorSeq    uint64
	runtimeProcessMu       sync.Mutex
	runtimeProcessSnapshot runtimeProcessSampler
	runtimeFileIOMu        sync.Mutex
	runtimeFileIO          runtimeFileIOSampler
	runtimeFileTrace       runtimeFileTraceMonitor
	runtimeDatabaseQueries databaseQueryMetrics
	runtimeControlMu       sync.Mutex
	runtimeControlStarted  bool
	runtimeControlDelay    time.Duration
	runtimeControlRunner   func()

	ocrRuntimeMu     sync.RWMutex
	ocrRuntime       *imageOCRRuntime
	ocrReconfigureMu sync.Mutex
	ocrLocalService  *ocr.LocalServiceManager

	ocrBackfillMu     sync.RWMutex
	ocrBackfill       imageOCRBackfillStatus
	ocrBackfillCancel context.CancelFunc
	ocrBackfillDone   chan struct{}
}

const (
	md5PathCacheLimit     = 20_000
	snsMediaKeyCacheLimit = 4_096
	statsCacheLimit       = 256
)

var runtimeAccessLogSkipPaths = []string{
	"/health",
	"/api/v1/runtime/status",
	"/api/v1/runtime/logs",
	"/api/v1/runtime/file-io/details",
	"/api/v1/runtime/file-io/trace/start",
	"/api/v1/runtime/file-io/trace/stop",
	"/api/v1/runtime/file-io/trace",
	"/api/v1/db/audit",
	"/api/v1/hook/status",
	"/api/v1/hook/events",
	"/api/v1/ocr/status",
	"/api/v1/events",
}

type statsCacheEntry struct {
	Payload   gin.H
	ExpiresAt time.Time
	Snapshot  *chatStatsSnapshot
}

type NetworkConfig interface {
	GetHTTPAddr() string
}

type MediaConfig interface {
	GetDataDir() string
	GetRuntimeDir() string
	GetSaveDecryptedMedia() bool
	GetDataKey() string
	GetWorkDir() string
	GetVersion() int
}

type HookConfig interface {
	GetMessageHook() *conf.MessageHook
	UpdateMessageHook(next conf.MessageHook) error
}

type OCRConfig interface {
	GetOCRConfig() conf.OCRConfig
}

type OCRConfigUpdater interface {
	UpdateOCRConfig(next conf.OCRConfig) error
}

type OCRConfigStorage interface {
	OCRConfigUpdater
	GetStoredOCRConfig() conf.OCRConfig
}

type Config interface {
	NetworkConfig
	MediaConfig
	HookConfig
}

type ImageKeyProvider interface {
	RefreshImageKey(dataDir string) (string, error)
}

func NewService(conf Config, db Database, imageKeys ImageKeyProvider, media ports.MediaCodec) *Service {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	s := &Service{
		conf:          conf,
		db:            db,
		imageKeys:     imageKeys,
		media:         media,
		router:        router,
		snsHTTPClient: newPublicHTTPClient(),
		events:        newSSEHub(),
	}
	s.resetRuntimeMetrics(time.Now())

	if err := router.SetTrustedProxies(nil); err != nil {
		log.Err(err).Msg("Failed to set trusted proxies")
	}

	router.Use(
		s.runtimeMetricsMiddleware(),
		errors.RecoveryMiddleware(),
		errors.ErrorHandlerMiddleware(),
		// Monitoring traffic (including the long-lived event stream) must not
		// create an access-log write stream merely by observing the service.
		gin.LoggerWithWriter(log.Logger, runtimeAccessLogSkipPaths...),
		securityHeadersMiddleware(),
		corsMiddleware(conf.GetHTTPAddr()),
		requestBodyLimitMiddleware(),
	)

	s.initRouter()
	return s
}

func (s *Service) Start() error {
	srv, listener, err := s.prepareServer()
	if err != nil {
		return err
	}
	if err := s.startOCRRuntime(); err != nil {
		log.Warn().Err(err).Msg("image OCR index is unavailable")
	}
	s.startEventStream()
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("HTTP server stopped unexpectedly")
		}
	}()
	log.Info().Msg("Starting HTTP server on " + listener.Addr().String())
	return nil
}

func (s *Service) ListenAndServe() error {
	srv, listener, err := s.prepareServer()
	if err != nil {
		return err
	}
	if err := s.startOCRRuntime(); err != nil {
		log.Warn().Err(err).Msg("image OCR index is unavailable")
	}
	defer s.stopOCRRuntime()
	s.startEventStream()
	defer s.stopEventStream()
	log.Info().Msg("Starting HTTP server on " + listener.Addr().String())
	err = srv.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Service) prepareServer() (*http.Server, net.Listener, error) {
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	if s.server != nil {
		return nil, nil, fmt.Errorf("HTTP server is already running")
	}
	addr := strings.TrimSpace(s.conf.GetHTTPAddr())
	if err := ValidateListenAddress(addr); err != nil {
		return nil, nil, err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	s.server = srv
	s.listen = listener
	s.resetRuntimeMetrics(time.Now())
	return srv, listener, nil
}

// IsRunning reports whether the HTTP server is currently serving requests.
func (s *Service) IsRunning() bool {
	if s == nil {
		return false
	}
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	return s.server != nil
}

func (s *Service) Stop() error {
	s.serverMu.Lock()
	srv := s.server
	listener := s.listen
	s.server = nil
	s.listen = nil
	s.serverMu.Unlock()
	s.stopRuntimeFileTrace()
	s.stopEventStream()
	if srv == nil {
		s.stopOCRRuntime()
		s.clearRuntimeCaches()
		s.closeIdleSNSConnections()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := srv.Shutdown(ctx)
	if err != nil && listener != nil {
		_ = listener.Close()
	}
	s.stopOCRRuntime()
	s.clearRuntimeCaches()
	s.closeIdleSNSConnections()
	if err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	log.Info().Msg("HTTP server stopped")
	return nil
}

func (s *Service) GetRouter() *gin.Engine {
	return s.router
}

func (s *Service) getStatsCache(key string) (gin.H, bool) {
	if strings.TrimSpace(key) == "" {
		return nil, false
	}
	now := time.Now()
	entry, ok := s.statsCache.Get(key)
	if !ok {
		return nil, false
	}
	if now.After(entry.ExpiresAt) {
		s.statsCache.Delete(key)
		return nil, false
	}
	return cloneStatsPayload(entry.Payload), true
}

func (s *Service) setStatsCache(key string, payload gin.H, ttl time.Duration) {
	if strings.TrimSpace(key) == "" || payload == nil {
		return
	}
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	now := time.Now()
	expireAt := now.Add(ttl)

	s.statsCache.DeleteIf(func(_ string, item statsCacheEntry) bool {
		if now.After(item.ExpiresAt) {
			return true
		}
		return false
	})
	s.statsCache.Set(key, statsCacheEntry{
		Payload:   cloneStatsPayload(payload),
		ExpiresAt: expireAt,
	}, statsCacheLimit)
}

func (s *Service) clearRuntimeCaches() {
	s.md5PathCache.Clear()
	s.snsMediaKeyCache.Clear()
	s.statsCache.Clear()
	s.databaseAuditMu.Lock()
	s.databaseAudit = nil
	s.databaseAuditShards = nil
	s.databaseAuditMu.Unlock()
}

func (s *Service) closeIdleSNSConnections() {
	s.snsHTTPClientMu.Lock()
	client := s.snsHTTPClient
	s.snsHTTPClientMu.Unlock()
	if client != nil {
		client.CloseIdleConnections()
	}
}

func cloneStatsPayload(in gin.H) gin.H {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		out := make(gin.H, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	var out gin.H
	if err := json.Unmarshal(raw, &out); err != nil {
		out = make(gin.H, len(in))
		for k, v := range in {
			out[k] = v
		}
	}
	return out
}
