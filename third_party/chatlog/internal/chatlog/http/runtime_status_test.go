package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
)

type runtimeStatusDatabaseFixture struct {
	Database
	state   ports.DatabaseState
	message string
	ready   bool
}

func (f runtimeStatusDatabaseFixture) Status() (ports.DatabaseState, string) {
	return f.state, f.message
}

func (f runtimeStatusDatabaseFixture) Ready() bool {
	return f.ready
}

func TestRuntimeStatusReportsLiveServiceAndDatabaseMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{
		conf: lifecycleConfig{},
		db: runtimeStatusDatabaseFixture{
			state: ports.DatabaseStateReady,
			ready: true,
		},
	}
	service.resetRuntimeMetrics(time.Now().Add(-3 * time.Second))

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/v1/runtime/status", nil)
	service.handleRuntimeStatus(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Service struct {
			Status        string  `json:"status"`
			UptimeSeconds float64 `json:"uptime_seconds"`
		} `json:"service"`
		Database struct {
			State string `json:"state"`
			Ready bool   `json:"ready"`
		} `json:"database"`
		Runtime struct {
			Goroutines int `json:"goroutines"`
		} `json:"runtime"`
		Memory struct {
			HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
		} `json:"memory"`
		FileIO struct {
			ProcessCount int                            `json:"process_count"`
			Processes    []runtimeFileIOProcessSnapshot `json:"processes"`
		} `json:"file_io"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Service.Status != "running" || response.Service.UptimeSeconds < 2 {
		t.Fatalf("unexpected service status: %+v", response.Service)
	}
	if response.Database.State != "ready" || !response.Database.Ready {
		t.Fatalf("unexpected database status: %+v", response.Database)
	}
	if response.Runtime.Goroutines < 1 || response.Memory.HeapAllocBytes == 0 {
		t.Fatalf("runtime metrics missing: runtime=%+v memory=%+v", response.Runtime, response.Memory)
	}
	if response.FileIO.ProcessCount < 1 || len(response.FileIO.Processes) < 1 {
		t.Fatalf("file I/O process metrics missing: %+v", response.FileIO)
	}
}

func TestRuntimeMetricsMiddlewareTracksBusinessRequestsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{}
	service.resetRuntimeMetrics(time.Now())
	router := gin.New()
	router.Use(service.runtimeMetricsMiddleware())
	router.GET("/api/v1/example", func(c *gin.Context) {
		c.Set(databaseRequestContextKey, true)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "fixture"})
	})
	router.POST("/api/v1/database-write", func(c *gin.Context) {
		c.Set(databaseRequestContextKey, true)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.GET("/api/v1/runtime/status", service.handleRuntimeStatus)
	router.GET("/static/example.js", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/example", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/database-write", strings.NewReader("fixture")),
		httptest.NewRequest(http.MethodGet, "/static/example.js", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/runtime/status", nil),
	}
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
	}

	if total := service.runtimeRequestsTotal.Load(); total != 2 {
		t.Fatalf("tracked requests = %d, want 2", total)
	}
	if count := service.runtimeStatus5xx.Load(); count != 1 {
		t.Fatalf("tracked 5xx responses = %d, want 1", count)
	}
	last, ok := service.runtimeLastRequest.Load().(runtimeLastRequestSnapshot)
	if !ok || last.Path != "/api/v1/database-write" || last.Status != http.StatusOK {
		t.Fatalf("unexpected last request: %+v", last)
	}
	if service.runtimeDatabaseRead.Load() == 0 || service.runtimeDatabaseReads.Load() != 2 {
		t.Fatalf("database reads = %d bytes / %d operations", service.runtimeDatabaseRead.Load(), service.runtimeDatabaseReads.Load())
	}
	if service.runtimeDatabaseWritten.Load() != uint64(len("fixture")) || service.runtimeDatabaseWrites.Load() != 1 {
		t.Fatalf("database writes = %d bytes / %d operations", service.runtimeDatabaseWritten.Load(), service.runtimeDatabaseWrites.Load())
	}
	errors := service.recentRuntimeHTTPErrors(10)
	if len(errors) != 1 || errors[0].Path != "/api/v1/example" || errors[0].Status != http.StatusServiceUnavailable {
		t.Fatalf("HTTP errors = %+v", errors)
	}
}

func TestFaviconProbeDoesNotCreateAnHTTP404(t *testing.T) {
	service := NewService(lifecycleConfig{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:12345"
	service.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("favicon status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}
