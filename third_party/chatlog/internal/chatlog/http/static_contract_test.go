package http

import (
	"io/fs"
	"strings"
	"testing"
)

func TestWebConsolePushStreamingAndSessionLimitControls(t *testing.T) {
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
		`id="session-limit"`,
		`params.append('limit', limit)`,
		`id="hook-poll-state"`,
		`id="hook-events-list"`,
		`new EventSource('/api/v1/events?topics=runtime,hook,ocr,log,message')`,
		`addEventListener('hook_events'`,
		`data.pending_deliveries`,
		`data.failed_deliveries`,
		`data.last_scan_duration_ms`,
		`evt.matched_rules`,
		`status === 'dead'`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("web console missing %q", expected)
		}
	}
}

func TestWebConsoleRuntimeFileIOMonitor(t *testing.T) {
	staticFS, err := fs.Sub(EFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	indexData, err := fs.ReadFile(staticFS, "index.htm")
	if err != nil {
		t.Fatal(err)
	}
	cssData, err := fs.ReadFile(staticFS, "console.css")
	if err != nil {
		t.Fatal(err)
	}
	index := string(indexData)
	for _, expected := range []string{
		`id="runtime-file-read-total"`,
		`id="runtime-file-write-total"`,
		`id="runtime-file-read-speed"`,
		`id="runtime-file-write-speed"`,
		`id="runtime-file-io-processes"`,
		`id="runtime-file-trace-records"`,
		`id="runtime-file-trace-kind"`,
		`id="runtime-file-trace-auto-stop-enabled"`,
		`id="runtime-file-trace-auto-stop-mb"`,
		`id="runtime-file-read-ranking"`,
		`id="runtime-file-write-ranking"`,
		`function renderRuntimeFileIOProcesses(fileIO)`,
		`function renderRuntimeFileTraceEvents(payload = {})`,
		`function renderRuntimeFileTraceRanking(containerID, ranking, kind)`,
		`/api/v1/runtime/file-io/details`,
		`/api/v1/runtime/file-io/trace/start`,
		`sample.file_io?.read_bytes_sec`,
		`sample.file_io?.write_bytes_sec`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("runtime file I/O dashboard is missing %q", expected)
		}
	}
	for _, stale := range []string{`runtimeDatabaseAuditRefreshMS`, `runtimeDatabaseAuditTimer`} {
		if strings.Contains(index, stale) {
			t.Errorf("dashboard still schedules automatic full database audits with %q", stale)
		}
	}
	css := string(cssData)
	for _, expected := range []string{
		`.runtime-file-io-panel`,
		`.runtime-file-process-command`,
		`.runtime-file-trace-table`,
		`.runtime-file-trace-path`,
		`overflow-x: auto`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("runtime file I/O styles are missing %q", expected)
		}
	}
}

func TestWebConsoleFormFieldsMatchHTTPContract(t *testing.T) {
	staticFS, err := fs.Sub(EFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	indexData, err := fs.ReadFile(staticFS, "index.htm")
	if err != nil {
		t.Fatal(err)
	}
	workspaceData, err := fs.ReadFile(staticFS, "workspace.js")
	if err != nil {
		t.Fatal(err)
	}
	index := string(indexData)
	workspace := string(workspaceData)

	for _, stale := range []string{
		`params.set('format', 'json')`,
		`正则匹配消息内容`,
		`type === 'sns'`,
		`时间和聊天对象是必填项`,
	} {
		if strings.Contains(index, stale) {
			t.Errorf("web console still contains stale contract fragment %q", stale)
		}
	}
	for _, expected := range []string{
		`if (parts.length === 3) return 'all';`,
		`按字面量匹配消息内容`,
		`function isDashboardSessionKind(item, kind)`,
		`chatType === 'private' || chatType === 'official_account'`,
		`dashboardSessions.filter(item => isDashboardSessionKind(item, kind))`,
		`normalizedIntegerInput('session-limit', 20, 1, 500)`,
		`if (!/^-?\d+$/.test(normalized))`,
		`return fail('请输入整数，不能包含小数或其他字符')`,
		`if (limit === null)`,
		`if(time) params.append('time', time);`,
		`isAbsoluteHTTPURL(postURL)`,
		`if (!postTarget.checked && !weixinTarget.checked && !qqTarget.checked)`,
		`if (!/^\d+$/.test(normalized)) return null;`,
		`notifyMode === 'all' || notifyMode.split(',').includes('post')`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("web console missing contract guard %q", expected)
		}
	}
	for _, stale := range []string{
		`let value = /^-?\d+$/.test(raw) ? Number.parseInt(raw, 10) : fallback`,
		`value = Math.max(minimum, value)`,
		`value = Math.min(maximum, value)`,
	} {
		if strings.Contains(index, stale) {
			t.Errorf("web console still silently normalizes invalid integer input with %q", stale)
		}
	}
	if !strings.Contains(workspace, `if (newMessageRequest) return newMessageRequest;`) {
		t.Error("message polling is missing its overlapping-request guard")
	}
	if !strings.Contains(workspace, `headers['X-Chatlog-State'] = JSON.stringify(newMessageState)`) {
		t.Error("message polling does not keep cursor state out of the request URL")
	}
	if strings.Contains(workspace, `params.set('state'`) {
		t.Error("message polling still exposes per-chat cursor state in the request URL")
	}
	for _, expected := range []string{
		`optionalInteger('mc-search-msg-type', 0)`,
		`optionalInteger('favorite-type', 0)`,
		`validRelativeDataPath(value('media-tool-data-path'))`,
		`absoluteHTTPURL(rawURL)`,
		`validUint64(key)`,
		`chat.toLowerCase().endsWith('@chatroom')`,
		`wxid.toLowerCase().endsWith('@openim')`,
	} {
		if !strings.Contains(workspace, expected) {
			t.Errorf("workspace form is missing server-side contract guard %q", expected)
		}
	}
}

func TestWebConsoleNumericAndPathControlsReflectAPIConstraints(t *testing.T) {
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
		`id="mc-search-msg-type" type="number" min="0" step="1"`,
		`id="media-tool-sns-key" type="text" inputmode="numeric" pattern="[0-9]*"`,
		`id="favorite-type" type="number" min="0" step="1"`,
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("web console control does not expose API constraint %q", expected)
		}
	}
}

func TestWebConsoleMessagePreviewsAndDeliveryActionsStayIntegrated(t *testing.T) {
	staticFS, err := fs.Sub(EFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	indexData, err := fs.ReadFile(staticFS, "index.htm")
	if err != nil {
		t.Fatal(err)
	}
	workspaceData, err := fs.ReadFile(staticFS, "workspace.js")
	if err != nil {
		t.Fatal(err)
	}
	index := string(indexData)
	workspace := string(workspaceData)
	for _, expected := range []string{
		`hookEventDeliveryAction`,
		`batchHookEventAction`,
		`hook-keyword-mode`,
		`<option value="mixed">`,
		`混合：文字 + 图片 OCR`,
		`hookSelectedEventIDs`,
		`'retry'`,
		`'cancel'`,
		`data-hook-context-event`,
		`window.chatlogCreateMessagePreview`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("web console delivery/message preview is missing %q", expected)
		}
	}
	for _, expected := range []string{
		`function createMessageBubble`,
		`function createMessageBody`,
		`function appendMessageLocationCard`,
		`function appendRedEnvelopeCard`,
		`function appendRedEnvelopeReceipt`,
		`contents.poiname`,
		`contents.red_envelope_title`,
		`contents.image_description`,
		`chat-image-description`,
		`contents.system_kind === 'red_envelope_receipt'`,
		`if (!references.length && Array.isArray(item.media_keys))`,
		`item.media_keys.some((key, index)`,
		`mediaElement(references[0], references.slice(1))`,
		`element.addEventListener('error'`,
		`case 34:`,
		`case 43:`,
		`case 47:`,
		`case 48:`,
		`case 49:`,
		`case 10000:`,
		`case 11000:`,
		`chat-preview-list`,
	} {
		if !strings.Contains(workspace, expected) {
			t.Errorf("message preview renderer is missing %q", expected)
		}
	}
	if strings.Contains(workspace, `references.slice(0, 4).forEach`) {
		t.Error("message preview still renders media lookup aliases as separate elements")
	}

	cssData, err := fs.ReadFile(staticFS, "console.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssData)
	for _, expected := range []string{
		`.chat-location-card`,
		`.chat-location-map`,
		`.chat-red-envelope-card`,
		`.chat-red-envelope-receipt-icon`,
		`.chat-image-description`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("message preview styles are missing %q", expected)
		}
	}
}

func TestWebConsoleSNSFeedUsesDedicatedRenderer(t *testing.T) {
	staticFS, err := fs.Sub(EFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	workspaceData, err := fs.ReadFile(staticFS, "workspace.js")
	if err != nil {
		t.Fatal(err)
	}
	cssData, err := fs.ReadFile(staticFS, "console.css")
	if err != nil {
		t.Fatal(err)
	}
	workspace := string(workspaceData)
	css := string(cssData)
	for _, expected := range []string{
		`function isSNSPostRecord`,
		`function createSNSPostCard`,
		`function appendSNSArticle`,
		`function appendSNSInteractions`,
		`media?.live_photo?.proxy_url`,
		`sameOriginMediaURL(item.avatar_url)`,
		`grid.className = messageCollection ? 'chat-preview-list'`,
		`: snsCollection ? 'sns-feed-list'`,
	} {
		if !strings.Contains(workspace, expected) {
			t.Errorf("SNS feed renderer is missing %q", expected)
		}
	}
	for _, expected := range []string{
		`.sns-post-card`,
		`.sns-article-card`,
		`.sns-media-grid.is-pair`,
		`.sns-live-badge`,
		`.sns-interactions`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("SNS feed styles are missing %q", expected)
		}
	}
}

func TestWebConsoleOCRUsesDedicatedOperationsPage(t *testing.T) {
	staticFS, err := fs.Sub(EFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	indexData, err := fs.ReadFile(staticFS, "index.htm")
	if err != nil {
		t.Fatal(err)
	}
	workspaceData, err := fs.ReadFile(staticFS, "workspace.js")
	if err != nil {
		t.Fatal(err)
	}
	cssData, err := fs.ReadFile(staticFS, "console.css")
	if err != nil {
		t.Fatal(err)
	}
	index := string(indexData)
	workspace := string(workspaceData)
	css := string(cssData)
	for _, expected := range []string{
		`switchTab('ocr-center')`,
		`id="ocr-center"`,
		`id="ocr-realtime-enabled"`,
		`id="ocr-backfill-enabled"`,
		`id="ocr-realtime-open"`,
		`id="ocr-realtime-close"`,
		`id="ocr-backfill-open"`,
		`id="ocr-backfill-close"`,
		`id="ocr-auto-download-open"`,
		`id="ocr-auto-download-close"`,
		`id="ocr-auto-download-enabled"`,
		`id="ocr-scope-mode"`,
		`id="ocr-contact-options"`,
		`id="ocr-chatroom-options"`,
		`id="ocr-task-list"`,
		`id="ocr-retry-waiting-all"`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("OCR operations page is missing %q", expected)
		}
	}
	for _, expected := range []string{
		`window.ocrCenterSaveConfig`,
		`window.ocrCenterSetPower`,
		`window.ocrCenterStartBackfill`,
		`window.ocrCenterStopBackfill`,
		`window.ocrCenterRetryWaitingMedia`,
		`window.ocrCenterRetryWaitingMediaAll`,
		`window.ocrCenterSearch`,
		`const leftSelected = selected.has(left.identifier) ? 0 : 1`,
		`renderOCRScopeOptions(type)`,
		`已选但当前目录未返回`,
		`const anyProcessRunning = backendReady || sdkReady`,
		`const pairSynchronized = local.synchronized !== false && backendReady === sdkReady`,
		`'模型同步启动中…'`,
		`'模型服务已同步运行'`,
		`'同步修复模型服务'`,
		`'模型服务已同步停止'`,
		`MLX 与 GLM-OCR SDK 均已运行`,
		`当前仅一端运行，点击后同步修复 MLX 与 GLM-OCR SDK`,
		`listen_contacts`,
		`listen_chatrooms`,
		`/api/v1/ocr/config`,
		`/api/v1/ocr/backfill/stop`,
	} {
		if !strings.Contains(workspace, expected) {
			t.Errorf("OCR operations behavior is missing %q", expected)
		}
	}
	for _, expected := range []string{
		`.ocr-control-hero`,
		`.ocr-status-grid`,
		`.ocr-scope-picker`,
		`.ocr-task-row`,
		`.ocr-technical-grid`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("OCR operations styles are missing %q", expected)
		}
	}
}
