package http

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

// EFS holds embedded file system data for static assets.
//
//go:embed static
var EFS embed.FS

func (s *Service) initRouter() {
	s.initBaseRouter()
	s.initMediaRouter()
	s.initHookRoutes()
	s.initCacheRoutes()
	api := s.router.Group("/api/v1", s.checkDBStateMiddleware())
	s.initChatRoutes(api)
	s.initSocialRoutes(api)
	s.initAddressBookRoutes(api)
	s.initDatabaseRoutes(api)
	s.initOCRRoutes(api)
}

func (s *Service) initBaseRouter() {
	staticDir, _ := fs.Sub(EFS, "static")

	s.router.StaticFS("/static", http.FS(staticDir))
	// The console currently has no branded icon asset. Answer the browser's
	// automatic favicon probe explicitly so it does not become a misleading 404
	// in the classified HTTP log.
	s.router.GET("/favicon.ico", func(ctx *gin.Context) {
		ctx.Header("Cache-Control", "public, max-age=86400")
		ctx.Status(http.StatusNoContent)
	})
	s.router.StaticFileFS("/", "./index.htm", http.FS(staticDir))

	s.router.GET("/health", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	// ping 不依赖数据库状态，放在中间件外层，保持可用性。
	s.router.GET("/api/v1/ping", s.handlePing)
	s.router.GET("/api/v1/events", s.handleEventStream)
	s.router.GET("/api/v1/runtime/status", s.handleRuntimeStatus)
	s.router.GET("/api/v1/runtime/logs", s.handleRuntimeLogs)
	s.router.GET("/api/v1/runtime/file-io/details", s.handleRuntimeFileTraceDetails)
	s.router.POST("/api/v1/runtime/file-io/trace/start", s.handleRuntimeFileTraceStart)
	s.router.POST("/api/v1/runtime/file-io/trace/stop", s.handleRuntimeFileTraceStop)
	s.router.DELETE("/api/v1/runtime/file-io/trace", s.handleRuntimeFileTraceClear)
	s.router.POST("/api/v1/runtime/terminate", s.handleRuntimeTerminate)
	s.router.GET("/api/v1/ocr/status", s.handleOCRStatus)
	s.router.GET("/api/v1/ocr/config", s.handleOCRConfigGet)
	s.router.PUT("/api/v1/ocr/config", s.handleOCRConfigSet)
	s.router.GET("/api/v1/meta", s.handleAPICatalog)
	s.router.GET("/api/v1/openapi.json", s.handleOpenAPI)

	s.router.NoRoute(s.NoRoute)
}

func (s *Service) initMediaRouter() {
	s.router.GET("/image/*key", func(c *gin.Context) { s.handleMedia(c, "image") })
	s.router.GET("/video/*key", func(c *gin.Context) { s.handleMedia(c, "video") })
	s.router.GET("/file/*key", func(c *gin.Context) { s.handleMedia(c, "file") })
	s.router.GET("/voice/*key", func(c *gin.Context) { s.handleMedia(c, "voice") })
	s.router.GET("/data/*path", s.handleMediaData)
}

func (s *Service) handlePing(c *gin.Context) {
	writeJSON(c, gin.H{"pong": true})
}
