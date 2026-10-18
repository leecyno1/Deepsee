package http

import (
	"io/fs"
	"strings"
	"testing"
)

// TestWebConsoleAccountsForEveryCatalogOperation is intentionally explicit.
// A new HTTP operation must either receive an interactive UI integration or be
// deliberately represented by a developer/indirect surface before this test
// can pass. This prevents silent Catalog <-> web-console drift.
func TestWebConsoleAccountsForEveryCatalogOperation(t *testing.T) {
	staticFS, err := fs.Sub(EFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	files := []string{"index.htm", "console.js", "workspace.js"}
	var source strings.Builder
	for _, name := range files {
		data, readErr := fs.ReadFile(staticFS, name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		source.Write(data)
		source.WriteByte('\n')
	}
	web := source.String()

	markers := map[string][]string{
		"api_meta":                    {"/api/v1/meta", "apiInspectorLoad"},
		"cache_clear":                 {"/api/v1/cache/clear", "clearRuntimeCache"},
		"cache_files":                 {"/api/v1/cache/files", "showRuntimeCacheFiles"},
		"cache_open":                  {"/api/v1/cache/open", "openRuntimeCachePath"},
		"cache_status":                {"fetch('/api/v1/cache'", "loadRuntimeCacheDetails"},
		"chatrooms":                   {"/api/v1/chatrooms", "chatroom-offset"},
		"contacts":                    {"/api/v1/contacts", "contact-offset"},
		"data":                        {"/data/", "mediaToolOpenData", "validRelativeDataPath"},
		"database_data":               {"/api/v1/db/data", "loadTableData"},
		"database_audit":              {"/api/v1/db/audit", "loadRuntimeDatabaseAudit"},
		"database_message_resources":  {"/api/v1/db/message_resources", "loadMessageResources"},
		"database_modules":            {"/api/v1/db/modules", "loadDatabaseBusinessModules"},
		"database_auxiliary":          {"/api/v1/db/auxiliary", "loadAuxiliaryDatasets"},
		"database_auxiliary_dataset":  {"/api/v1/db/auxiliary/", "loadAuxiliaryDataset"},
		"database_query":              {"/api/v1/db/query", "runSQL"},
		"database_search":             {"/api/v1/db/search", "runGlobalSearch"},
		"database_search_consistency": {"/api/v1/db/search/consistency", "loadSearchConsistency"},
		"database_tables":             {"/api/v1/db/tables", "openDBViewer"},
		"databases":                   {"fetch('/api/v1/db')", "loadDBList"},
		"favorites":                   {"/api/v1/favorites", "socialLoadFavorites", "optionalInteger('favorite-type', 0)"},
		"file":                        {"media-tool-type", `<option value="file">`, "mediaToolOpenMessage"},
		"health":                      {"fetch('/health'", "refreshServiceStatus"},
		"history":                     {"/api/v1/history", "queryAPI('chatlog')"},
		"hook_config_get":             {"loadHookConfig", "fetch('/api/v1/hook/config')"},
		"hook_config_set":             {"saveHookConfig", "method: 'POST'", "/api/v1/hook/config", "!postTarget.checked && !weixinTarget.checked && !qqTarget.checked"},
		"hook_events":                 {"loadHookEvents", "/api/v1/hook/events?limit=50"},
		"hook_events_clear":           {"clearHookEvents", "/api/v1/hook/events/clear"},
		"hook_events_batch_action":    {"batchHookEventAction", "/api/v1/hook/events/batch-action", "event_ids"},
		"hook_event_action":           {"/api/v1/hook/events/", "hookEventDeliveryAction", "'retry'", "'cancel'", "'delete'"},
		"hook_hermes_qq_get":          {"loadHermesQQConfig", "/api/v1/hook/hermes/qq"},
		"hook_hermes_qq_set":          {"saveHermesQQConfig", "home_channel_name", "/api/v1/hook/hermes/qq"},
		"hook_hermes_weixin_get":      {"loadHermesWeixinConfig", "/api/v1/hook/hermes/weixin"},
		"hook_hermes_weixin_set":      {"saveHermesWeixinConfig", "cdn_base_url", "/api/v1/hook/hermes/weixin"},
		"hook_status":                 {"loadHookStatus", "/api/v1/hook/status"},
		"image":                       {`<option value="image">`, "mediaToolOpenMessage"},
		"keyword_analytics":           {"/api/v1/analytics/keywords", "loadKeywordAnalytics", "search_path"},
		"members":                     {"/api/v1/members", "messageCenterLoadMembers", "endsWith('@chatroom')"},
		"new_messages":                {"/api/v1/new_messages", "messageCenterLoadNew"},
		"openapi":                     {"/api/v1/openapi.json", "apiInspectorDownloadOpenAPI"},
		"ocr_status":                  {"/api/v1/ocr/status", "loadOCRStatus", "ocr-task-list"},
		"ocr_config_get":              {"/api/v1/ocr/config", "loadOCRConfig", "ocr-scope-mode"},
		"ocr_config_set":              {"/api/v1/ocr/config", "ocrCenterSaveConfig", "backfill_enabled", "auto_download_original", "listen_chatrooms", "retry_sync"},
		"ocr_search":                  {"/api/v1/ocr/search", "ocrCenterSearch", "ocr-search-keyword"},
		"ocr_backfill":                {"/api/v1/ocr/backfill", "ocrCenterStartBackfill", "respect_scope"},
		"ocr_backfill_stop":           {"/api/v1/ocr/backfill/stop", "ocrCenterStopBackfill"},
		"ocr_record_get":              {"/api/v1/ocr/index/", "ocrCenterOpenDetail", "ocr-detail-modal"},
		"ocr_succeeded_clear":         {"/api/v1/ocr/index/succeeded", "ocrCenterClearSucceeded"},
		"ocr_waiting_media_retry_all": {"/api/v1/ocr/index/waiting-media/retry", "ocrCenterRetryWaitingMediaAll"},
		"ocr_waiting_media_retry":     {"/media-retry", "ocrCenterRetryWaitingMedia"},
		"ocr_retry":                   {"/api/v1/ocr/index/", "ocrCenterRetry"},
		"ocr_rerecognize":             {"/api/v1/ocr/index/", "ocrCenterRerecognize", "/rerecognize"},
		"ocr_local_service_start":     {"/api/v1/ocr/local-service/", "ocrCenterLocalServiceAction", "'start'"},
		"ocr_local_service_stop":      {"/api/v1/ocr/local-service/", "ocrCenterLocalServiceAction", "'stop'"},
		"openim_corp":                 {"/api/v1/openim/", "contactLoadOpenIMCorp", "endsWith('@openim')"},
		"ping":                        {"/api/v1/ping", "apiInspectorLoad"},
		"events":                      {"/api/v1/events", "EventSource"},
		"runtime_status":              {"/api/v1/runtime/status", "loadRuntimeDashboard"},
		"runtime_logs":                {"/api/v1/runtime/logs", "loadRuntimeLogs"},
		"runtime_file_io_details":     {"/api/v1/runtime/file-io/details", "loadRuntimeFileTraceDetails"},
		"runtime_file_io_trace_start": {"/api/v1/runtime/file-io/trace/start", "startRuntimeFileTrace"},
		"runtime_file_io_trace_stop":  {"/api/v1/runtime/file-io/trace/stop", "stopRuntimeFileTrace"},
		"runtime_file_io_trace_clear": {"/api/v1/runtime/file-io/trace", "clearRuntimeFileTrace"},
		"runtime_terminate":           {"/api/v1/runtime/terminate", "terminateChatlogAndRestartWeChat", "terminate-and-restart"},
		"search":                      {"/api/v1/search", "messageCenterSearch", "optionalInteger('mc-search-msg-type', 0)"},
		"sessions":                    {"/api/v1/sessions", "session-limit"},
		"sns_feed":                    {"/api/v1/sns_feed", "socialLoadFeed"},
		"sns_media_proxy":             {"/api/v1/sns/media/proxy", "mediaToolOpenSNS", "absoluteHTTPURL(rawURL)", "validUint64(key)"},
		"sns_notifications":           {"/api/v1/sns_notifications", "socialLoadNotifications"},
		"sns_search":                  {"/api/v1/sns_search", "socialSearch"},
		"stats":                       {"/api/v1/stats", "loadDashboardStats"},
		"unread":                      {"/api/v1/unread", "messageCenterLoadUnread"},
		"video":                       {`<option value="video">`, "mediaToolOpenMessage"},
		"voice":                       {`<option value="voice">`, "mediaToolOpenMessage"},
	}

	catalogNames := make(map[string]bool)
	for _, endpoint := range EndpointCatalog() {
		catalogNames[endpoint.Name] = true
		required, ok := markers[endpoint.Name]
		if !ok {
			t.Errorf("web console has no integration decision for catalog operation %q", endpoint.Name)
			continue
		}
		for _, marker := range required {
			if !strings.Contains(web, marker) {
				t.Errorf("web console integration %q is missing source marker %q", endpoint.Name, marker)
			}
		}
	}
	for name := range markers {
		if !catalogNames[name] {
			t.Errorf("web console integration references removed catalog operation %q", name)
		}
	}
}

func TestWebConsoleFeaturePanels(t *testing.T) {
	staticFS, err := fs.Sub(EFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(staticFS, "index.htm")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, expected := range []string{
		`id="dashboard"`,
		`id="analytics"`,
		`id="database"`,
		`<h2>项目运行仪表盘</h2>`,
		`<h2>聊天数据分析</h2>`,
		`<h2 id="database-section-title">数据库</h2>`,
		`id="runtime-trend-cpu"`,
		`id="runtime-diagnostics"`,
		`id="runtime-db-read-total"`,
		`id="runtime-query-p95"`,
		`id="runtime-slow-queries"`,
		`id="runtime-database-audit"`,
		`id="message-resource-result"`,
		`id="db-auxiliary-datasets"`,
		`id="global-search-consistency"`,
		`id="runtime-log-list"`,
		`id="runtime-http-error-records"`,
		`id="runtime-cache-categories"`,
		`id="runtime-cache-files"`,
		`id="runtime-terminate-button"`,
		`id="db-business-result"`,
		`id="message-center"`,
		`id="mc-search-result"`,
		`id="mc-unread-result"`,
		`id="mc-new-result"`,
		`id="mc-members-result"`,
		`id="media-tool-preview"`,
		`id="sns-search-result"`,
		`id="sns-notification-result"`,
		`id="favorite-result"`,
		`id="contact-openim-result"`,
		`id="api-inspector"`,
		`id="ocr-route-title"`,
		`id="ocr-deploy-guide-mlx"`,
		`id="ocr-deploy-guide-vllm"`,
		`ocrCenterCopyDeployment('mlx')`,
		`ocrCenterCopyDeployment('vllm')`,
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("web console missing feature panel %q", expected)
		}
	}
}
