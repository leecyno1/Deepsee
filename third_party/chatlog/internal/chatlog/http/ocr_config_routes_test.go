package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ocr"
)

type ocrConfigRouteFixture struct {
	lifecycleConfig
	config  conf.OCRConfig
	workDir string
}

func (f *ocrConfigRouteFixture) GetWorkDir() string {
	return f.workDir
}

func (f *ocrConfigRouteFixture) GetOCRConfig() conf.OCRConfig {
	return conf.NormalizeOCRConfig(f.config)
}

func (f *ocrConfigRouteFixture) GetStoredOCRConfig() conf.OCRConfig {
	return conf.NormalizeOCRConfig(f.config)
}

func (f *ocrConfigRouteFixture) UpdateOCRConfig(next conf.OCRConfig) error {
	f.config = conf.NormalizeOCRConfig(next)
	return nil
}

func TestOCRConfigRoutePersistsModeSwitchesAndScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fixture := &ocrConfigRouteFixture{
		config:  conf.DefaultOCRConfig(),
		workDir: t.TempDir(),
	}
	service := &Service{conf: fixture}
	body := `{
		"enabled": false,
		"backfill_enabled": false,
		"local_auto_start": false,
		"local_auto_restart": false,
		"mode": "local",
		"endpoint": "http://127.0.0.1:8081/glmocr/parse",
		"model": "glm-ocr",
		"poll_interval_sec": 7,
		"request_timeout_sec": 120,
		"received_only": true,
		"scope_all": false,
		"listen_contacts": ["wxid_a", "wxid_b"],
		"listen_chatrooms": ["100@chatroom"]
	}`
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/ocr/config", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	service.handleOCRConfigSet(ctx)
	defer service.stopOCRRuntime()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.config.PollIntervalSec != 7 ||
		fixture.config.RequestTimeout != 120 ||
		fixture.config.LocalAutoStartEnabled() ||
		fixture.config.LocalAutoRestartEnabled() ||
		!fixture.config.AllowsTalker("wxid_a") ||
		!fixture.config.AllowsTalker("100@chatroom") ||
		fixture.config.AllowsTalker("wxid_other") {
		t.Fatalf("saved config = %#v", fixture.config)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, exposed := response["api_key"]; exposed {
		t.Fatal("OCR config response exposed API key")
	}
	if response["mode"] != "local" || response["scope_all"] != false {
		t.Fatalf("response = %#v", response)
	}
}

func TestOCRConfigRouteRequiresKeyForEnabledAPIMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fixture := &ocrConfigRouteFixture{
		config:  conf.DefaultOCRConfig(),
		workDir: t.TempDir(),
	}
	service := &Service{conf: fixture}
	body := `{
		"enabled": true,
		"backfill_enabled": false,
		"mode": "api",
		"endpoint": "https://open.bigmodel.cn/api/paas/v4/layout_parsing",
		"model": "glm-ocr",
		"poll_interval_sec": 5,
		"request_timeout_sec": 180,
		"received_only": true,
		"scope_all": true
	}`
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/ocr/config", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	service.handleOCRConfigSet(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOCRBackfillModeRunsWorkerWithoutRealtimeScanner(t *testing.T) {
	config := conf.DefaultOCRConfig()
	config.Enabled = false
	config.BackfillEnabled = true
	config.LocalAutoStart = boolPointer(false)
	config.LocalAutoRestart = boolPointer(false)
	fixture := &ocrConfigRouteFixture{config: config, workDir: t.TempDir()}
	service := &Service{conf: fixture}
	if err := service.startOCRRuntime(); err != nil {
		t.Fatal(err)
	}
	defer service.stopOCRRuntime()
	status := service.ocrStatus(t.Context())
	if status.RealtimeRunning || !status.WorkerRunning || !status.Running {
		t.Fatalf("status = %#v", status)
	}
	if !status.Backfill.Enabled || !status.Backfill.ReceivedOnly || !status.Backfill.RespectScope {
		t.Fatalf("backfill status = %#v", status.Backfill)
	}
}

func TestOCRConfigModeSwitchRequeuesUnfinishedTasksForNewProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fixture := &ocrConfigRouteFixture{
		config:  conf.DefaultOCRConfig(),
		workDir: t.TempDir(),
	}
	service := &Service{conf: fixture}
	store, err := ocr.OpenStore(service.ocrIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	ref := ocr.ImageRef{
		Talker: "wxid_fixture", MessageTime: 1_785_000_500,
		DBLocalID: 5, MediaKey: "retry-after-mode-switch",
	}
	id, _, err := store.Enqueue(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessing(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(context.Background(), id, errors.New("old local route failed")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	body := `{
		"enabled": false,
		"backfill_enabled": false,
		"mode": "api",
		"endpoint": "https://open.bigmodel.cn/api/paas/v4/layout_parsing",
		"api_key": "fixture-key",
		"model": "glm-ocr",
		"poll_interval_sec": 5,
		"request_timeout_sec": 180,
		"received_only": true,
		"scope_all": true
	}`
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/ocr/config", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	service.handleOCRConfigSet(ctx)
	defer service.stopOCRRuntime()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		RetrySync struct {
			Changed  bool   `json:"changed"`
			Requeued int64  `json:"requeued"`
			Provider string `json:"provider"`
		} `json:"retry_sync"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.RetrySync.Changed || response.RetrySync.Requeued != 1 ||
		response.RetrySync.Provider != conf.OCRProviderMaaS {
		t.Fatalf("retry sync = %#v", response.RetrySync)
	}
	runtime := service.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		t.Fatal("OCR runtime store was not restarted")
	}
	record, err := runtime.store.Find(context.Background(), ref)
	if err != nil || record == nil || record.Status != ocr.StatusPending ||
		record.Provider != conf.OCRProviderMaaS || record.Model != "glm-ocr" ||
		record.Error != "" || record.NextAttemptAt != 0 {
		t.Fatalf("record = %#v err=%v", record, err)
	}
}

func TestSetOCRLocalAutoRestartPersistsExplicitStopPolicy(t *testing.T) {
	config := conf.DefaultOCRConfig()
	config.LocalAutoRestart = boolPointer(true)
	fixture := &ocrConfigRouteFixture{config: config, workDir: t.TempDir()}
	service := &Service{conf: fixture}
	saved, err := service.setOCRLocalAutoRestart(false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.LocalAutoRestartEnabled() || fixture.config.LocalAutoRestartEnabled() {
		t.Fatalf("saved=%#v fixture=%#v", saved, fixture.config)
	}
}

func TestOCRRerecognizeRouteRequeuesSucceededRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config := conf.DefaultOCRConfig()
	config.Enabled = false
	config.BackfillEnabled = false
	fixture := &ocrConfigRouteFixture{config: config, workDir: t.TempDir()}
	service := &Service{conf: fixture}
	if err := service.startOCRRuntime(); err != nil {
		t.Fatal(err)
	}
	defer service.stopOCRRuntime()
	runtime := service.currentOCRRuntime()
	ref := ocr.ImageRef{
		Talker: "wxid_fixture", MessageTime: 1_785_001_000,
		DBLocalID: 10, MediaKey: "route-rerecognize-fixture",
	}
	id, _, err := runtime.store.Enqueue(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.store.MarkProcessing(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := runtime.store.MarkSucceeded(
		t.Context(), id, "hash", "vllm", "old-model", "旧结果", "旧文字", "旧版面", nil,
	); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/ocr/index/1/rerecognize", nil)
	service.handleOCRRerecognize(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	record, err := runtime.store.Find(t.Context(), ref)
	if err != nil || record == nil || record.Status != ocr.StatusPending ||
		record.Provider != config.Provider || record.Model != config.Model || record.OCRText != "" {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}

func TestOCRRecordDetailAndClearSucceededRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config := conf.DefaultOCRConfig()
	config.Enabled = false
	config.BackfillEnabled = false
	fixture := &ocrConfigRouteFixture{config: config, workDir: t.TempDir()}
	service := &Service{conf: fixture}
	if err := service.startOCRRuntime(); err != nil {
		t.Fatal(err)
	}
	defer service.stopOCRRuntime()
	runtime := service.currentOCRRuntime()
	ref := ocr.ImageRef{
		Talker: "wxid_fixture", TalkerName: "测试会话", MessageTime: 1_785_003_000,
		DBLocalID: 30, MediaKey: "route-detail-fixture", MediaPath: "msg/image.dat",
	}
	id, _, err := runtime.store.Enqueue(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.store.MarkSucceeded(
		t.Context(), id, "hash", "vllm", "glm-ocr", "完整描述", "完整 OCR 文字", "# 完整 OCR 文字", json.RawMessage(`[{"label":"text"}]`),
	); err != nil {
		t.Fatal(err)
	}

	detailRecorder := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailRecorder)
	detailContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/ocr/index/1", nil)
	service.handleOCRRecordGet(detailContext)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["ocr_text"] != "完整 OCR 文字" || detail["markdown"] != "# 完整 OCR 文字" || detail["layout"] == nil || detail["image_url"] == nil {
		t.Fatalf("detail=%#v", detail)
	}
	for _, privateField := range []string{"aes_key", "cdn_mid_url", "cdn_big_url", "cdn_thumb_url"} {
		if _, exposed := detail[privateField]; exposed {
			t.Fatalf("detail exposed %s", privateField)
		}
	}

	clearRecorder := httptest.NewRecorder()
	clearContext, _ := gin.CreateTestContext(clearRecorder)
	clearContext.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/ocr/index/succeeded", nil)
	service.handleOCRSucceededClear(clearContext)
	if clearRecorder.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", clearRecorder.Code, clearRecorder.Body.String())
	}
	var cleared struct {
		Removed int64 `json:"removed"`
	}
	if err := json.Unmarshal(clearRecorder.Body.Bytes(), &cleared); err != nil || cleared.Removed != 1 {
		t.Fatalf("cleared=%#v err=%v", cleared, err)
	}
}
