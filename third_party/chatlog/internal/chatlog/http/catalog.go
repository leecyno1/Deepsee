package http

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// APIParameter describes one value accepted by an HTTP endpoint. The same
// catalog is consumed by the server and the LLM-oriented CLI, so command
// aliases cannot silently drift away from the actual router.
type APIParameter struct {
	Name        string   `json:"name"`
	In          string   `json:"in"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Minimum     *float64 `json:"minimum,omitempty"`
	Maximum     *float64 `json:"maximum,omitempty"`
	Example     string   `json:"example,omitempty"`
}

// APIValueSchema is the JSON Schema subset used by request bodies and the
// generated OpenAPI document.
type APIValueSchema struct {
	Type        string                    `json:"type,omitempty"`
	Format      string                    `json:"format,omitempty"`
	Description string                    `json:"description,omitempty"`
	Enum        []string                  `json:"enum,omitempty"`
	Minimum     *float64                  `json:"minimum,omitempty"`
	Maximum     *float64                  `json:"maximum,omitempty"`
	Properties  map[string]APIValueSchema `json:"properties,omitempty"`
	Required    []string                  `json:"required,omitempty"`
	Items       *APIValueSchema           `json:"items,omitempty"`
	Example     interface{}               `json:"example,omitempty"`
}

type APIRequestBody struct {
	Required    bool           `json:"required"`
	ContentType string         `json:"content_type"`
	Schema      APIValueSchema `json:"schema"`
}

// APIResponseSpec tells agents whether stdout can remain structured or an
// output file is required. Mode is one of json, binary, media, or format.
type APIResponseSpec struct {
	Mode             string   `json:"mode"`
	Description      string   `json:"description"`
	ContentTypes     []string `json:"content_types"`
	SuccessStatus    int      `json:"success_status"`
	ControlParameter string   `json:"control_parameter,omitempty"`
	BinaryValues     []string `json:"binary_values,omitempty"`
}

// EndpointSpec is the stable, machine-readable contract for one endpoint.
type EndpointSpec struct {
	Name             string          `json:"name"`
	Method           string          `json:"method"`
	Path             string          `json:"path"`
	Category         string          `json:"category"`
	Summary          string          `json:"summary"`
	Parameters       []APIParameter  `json:"parameters,omitempty"`
	RequestBody      *APIRequestBody `json:"request_body,omitempty"`
	Response         APIResponseSpec `json:"response"`
	RequiresDatabase bool            `json:"requires_database,omitempty"`
	SideEffect       bool            `json:"side_effect,omitempty"`
	LocalOnly        bool            `json:"local_only,omitempty"`
	Confirmation     bool            `json:"confirmation_required,omitempty"`
}

func query(name, typ, def, description string, required bool) APIParameter {
	return APIParameter{Name: name, In: "query", Type: typ, Required: required, Default: def, Description: description}
}

func queryExample(name, typ, def, description, example string, required bool) APIParameter {
	parameter := query(name, typ, def, description, required)
	parameter.Example = example
	return parameter
}

func pathParam(name, description string) APIParameter {
	return APIParameter{Name: name, In: "path", Type: "string", Required: true, Description: description}
}

func headerParam(name, typ, description string, required bool) APIParameter {
	return APIParameter{Name: name, In: "header", Type: typ, Required: required, Description: description}
}

func queryEnum(name, def, description string, required bool, values ...string) APIParameter {
	parameter := query(name, "string", def, description, required)
	parameter.Enum = append([]string(nil), values...)
	return parameter
}

func queryInt(name, def, description string, required bool, minimum, maximum *float64) APIParameter {
	parameter := query(name, "integer", def, description, required)
	parameter.Minimum = minimum
	parameter.Maximum = maximum
	return parameter
}

func number(value float64) *float64 {
	result := value
	return &result
}

func jsonResponse(description string) APIResponseSpec {
	return APIResponseSpec{Mode: "json", Description: description, ContentTypes: []string{"application/json"}, SuccessStatus: http.StatusOK}
}

func binaryResponse(description string, contentTypes ...string) APIResponseSpec {
	return APIResponseSpec{Mode: "binary", Description: description, ContentTypes: append([]string(nil), contentTypes...), SuccessStatus: http.StatusOK}
}

func mediaResponse(description string) APIResponseSpec {
	return APIResponseSpec{Mode: "media", Description: description, ContentTypes: []string{"application/json", "application/octet-stream", "image/*", "audio/*", "video/*"}, SuccessStatus: http.StatusOK, ControlParameter: "info"}
}

func formatResponse(description string) APIResponseSpec {
	return APIResponseSpec{Mode: "format", Description: description, ContentTypes: []string{"application/json", "text/csv", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}, SuccessStatus: http.StatusOK, ControlParameter: "format", BinaryValues: []string{"csv", "xlsx", "excel"}}
}

func acceptedResponse(description string) APIResponseSpec {
	response := jsonResponse(description)
	response.SuccessStatus = http.StatusAccepted
	return response
}

func objectRequestBody(required []string, properties map[string]APIValueSchema, example map[string]interface{}) *APIRequestBody {
	return &APIRequestBody{Required: true, ContentType: "application/json", Schema: APIValueSchema{Type: "object", Properties: properties, Required: append([]string(nil), required...), Example: example}}
}

func stringSchema(description string, enum ...string) APIValueSchema {
	return APIValueSchema{Type: "string", Description: description, Enum: append([]string(nil), enum...)}
}

func integerSchema(description string, minimum, maximum *float64) APIValueSchema {
	return APIValueSchema{Type: "integer", Description: description, Minimum: minimum, Maximum: maximum}
}

func numberSchema(description string, minimum, maximum *float64) APIValueSchema {
	return APIValueSchema{Type: "number", Description: description, Minimum: minimum, Maximum: maximum}
}

func booleanSchema(description string) APIValueSchema {
	return APIValueSchema{Type: "boolean", Description: description}
}

func stringArraySchema(description string) APIValueSchema {
	item := stringSchema("")
	return APIValueSchema{Type: "array", Description: description, Items: &item}
}

var (
	ocrConfigRequestBody = objectRequestBody(
		[]string{"enabled", "backfill_enabled", "mode", "endpoint", "model", "poll_interval_sec", "request_timeout_sec", "received_only", "scope_all"},
		map[string]APIValueSchema{
			"enabled":                booleanSchema("开启实时 OCR"),
			"backfill_enabled":       booleanSchema("开启历史补录能力"),
			"auto_download_original": booleanSchema("全部会话直接升级；指定范围按 CDN 文件标识精准触发（实验）"),
			"local_auto_start":       booleanSchema("本地模型跟随主进程自动启动"),
			"local_auto_restart":     booleanSchema("本地模型异常退出后自动重启"),
			"mode":                   stringSchema("识别模式", "local", "api"),
			"provider":               stringSchema("底层提供方", "vllm", "maas"),
			"endpoint":               {Type: "string", Format: "uri", Description: "OCR 服务地址"},
			"api_key":                stringSchema("API 模式密钥；空值保留原配置"),
			"clear_api_key":          booleanSchema("清除已保存的 API Key"),
			"model":                  stringSchema("模型名称"),
			"poll_interval_sec":      integerSchema("兼容配置字段；实时链路由数据库变化事件触发", number(1), number(3600)),
			"request_timeout_sec":    integerSchema("单次识别超时", number(10), number(1800)),
			"received_only":          booleanSchema("仅处理收到的图片"),
			"scope_all":              booleanSchema("监听全部会话"),
			"listen_contacts":        stringArraySchema("指定联系人 wxid"),
			"listen_chatrooms":       stringArraySchema("指定群聊 ID"),
		},
		map[string]interface{}{"enabled": true, "backfill_enabled": false, "auto_download_original": false, "local_auto_start": true, "local_auto_restart": true, "mode": "local", "endpoint": "http://127.0.0.1:8081/glmocr/parse", "model": "glm-ocr", "poll_interval_sec": 5, "request_timeout_sec": 180, "received_only": true, "scope_all": true, "listen_contacts": []string{}, "listen_chatrooms": []string{}},
	)
	hookConfigRequestBody = objectRequestBody(
		[]string{"notify_mode"},
		map[string]APIValueSchema{
			"keywords":          stringSchema("使用 ｜ 分隔的关键词"),
			"keyword_mode":      stringSchema("关键词匹配消息类型", "text", "image_ocr", "mixed"),
			"notify_mode":       stringSchema("投递渠道组合", "post", "weixin", "qq", "all", "post,weixin", "post,qq", "weixin,qq"),
			"post_url":          {Type: "string", Format: "uri", Description: "POST Webhook 地址"},
			"before_count":      integerSchema("前文条数", number(0), nil),
			"after_count":       integerSchema("后文条数", number(0), nil),
			"forward_all":       booleanSchema("转发所有新消息"),
			"forward_contacts":  stringSchema("联系人列表"),
			"forward_chatrooms": stringSchema("群聊列表"),
		},
		map[string]interface{}{"keywords": "到账｜告警", "keyword_mode": "text", "notify_mode": "post", "post_url": "http://127.0.0.1:8080/webhook", "before_count": 5, "after_count": 5, "forward_all": false, "forward_contacts": "", "forward_chatrooms": ""},
	)
	hermesWeixinRequestBody = objectRequestBody(
		nil,
		map[string]APIValueSchema{
			"hermes_home":       stringSchema("Hermes Home 路径"),
			"account_id":        stringSchema("微信 account ID"),
			"token":             stringSchema("微信 bot token；空值保留原配置"),
			"base_url":          {Type: "string", Format: "uri"},
			"cdn_base_url":      {Type: "string", Format: "uri"},
			"home_channel":      stringSchema("Home Channel ID"),
			"home_channel_name": stringSchema("Home Channel 名称"),
		},
		map[string]interface{}{"hermes_home": "", "account_id": "", "token": "", "base_url": "", "cdn_base_url": "", "home_channel": "", "home_channel_name": ""},
	)
	hermesQQRequestBody = objectRequestBody(
		nil,
		map[string]APIValueSchema{
			"hermes_home":       stringSchema("Hermes Home 路径"),
			"app_id":            stringSchema("QQ Bot App ID"),
			"client_secret":     stringSchema("QQ Bot secret；空值保留原配置"),
			"home_channel":      stringSchema("Home Channel；私聊使用 user OpenID，群聊使用 group:group_openid，频道使用 channel:channel_id"),
			"home_channel_name": stringSchema("Home Channel 名称"),
		},
		map[string]interface{}{"hermes_home": "", "app_id": "", "client_secret": "", "home_channel": "group:group_openid", "home_channel_name": ""},
	)
)

var endpointCatalog = []EndpointSpec{
	{Name: "health", Method: http.MethodGet, Path: "/health", Category: "system", Summary: "进程健康检查"},
	{Name: "ping", Method: http.MethodGet, Path: "/api/v1/ping", Category: "system", Summary: "JSON API 连通性检查"},
	{Name: "events", Method: http.MethodGet, Path: "/api/v1/events", Category: "system", Summary: "复用单连接实时推送运行状态、Hook、OCR、日志与新消息事件", Parameters: []APIParameter{query("topics", "string", "runtime,hook,ocr,log,message", "逗号分隔事件主题", false), query("last_event_id", "integer", "", "断线续传事件 ID；也可使用 Last-Event-ID 请求头", false)}, Response: APIResponseSpec{Mode: "binary", Description: "Server-Sent Events 流", ContentTypes: []string{"text/event-stream"}, SuccessStatus: http.StatusOK}},
	{Name: "runtime_status", Method: http.MethodGet, Path: "/api/v1/runtime/status", Category: "system", Summary: "实时运行状态与性能指标"},
	{Name: "runtime_logs", Method: http.MethodGet, Path: "/api/v1/runtime/logs", Category: "system", Summary: "分类读取最近运行日志", Parameters: []APIParameter{queryEnum("category", "all", "日志分类", false, "all", "system", "http", "database", "cache", "media", "push"), queryEnum("level", "all", "日志级别", false, "all", "debug", "info", "warn", "error"), queryInt("limit", "100", "返回日志条数", false, number(1), number(500))}},
	{Name: "runtime_file_io_details", Method: http.MethodGet, Path: "/api/v1/runtime/file-io/details", Category: "system", Summary: "读取项目关联进程最近逐文件读写事件", Parameters: []APIParameter{queryEnum("kind", "all", "事件类型", false, "all", "read", "write", "metadata"), query("query", "string", "", "文件路径、进程或操作过滤", false), queryInt("limit", "200", "返回事件数", false, number(1), number(500))}},
	{Name: "runtime_file_io_trace_start", Method: http.MethodPost, Path: "/api/v1/runtime/file-io/trace/start", Category: "system", Summary: "经 macOS 管理员授权开启逐文件内核事件追踪，可按本轮累计写入阈值自动暂停", SideEffect: true, LocalOnly: true, RequestBody: objectRequestBody(nil, map[string]APIValueSchema{"auto_stop_enabled": booleanSchema("启用写入阈值自动暂停"), "auto_stop_write_mb": numberSchema("本轮累计写入触发阈值，单位 MB", number(0.1), number(runtimeFileTraceMaxAutoStopWriteMB))}, map[string]interface{}{"auto_stop_enabled": false, "auto_stop_write_mb": runtimeFileTraceDefaultAutoStopWriteMB})},
	{Name: "runtime_file_io_trace_stop", Method: http.MethodPost, Path: "/api/v1/runtime/file-io/trace/stop", Category: "system", Summary: "停止逐文件内核事件追踪并保留内存记录", SideEffect: true, LocalOnly: true},
	{Name: "runtime_file_io_trace_clear", Method: http.MethodDelete, Path: "/api/v1/runtime/file-io/trace", Category: "system", Summary: "清空逐文件读写内存记录", SideEffect: true, LocalOnly: true},
	{Name: "runtime_terminate", Method: http.MethodPost, Path: "/api/v1/runtime/terminate", Category: "system", Summary: "结束 Chatlog 及关联进程、释放注入并重启微信", SideEffect: true, LocalOnly: true, Confirmation: true, RequestBody: objectRequestBody([]string{"confirmation"}, map[string]APIValueSchema{"confirmation": stringSchema("固定确认值", "terminate-and-restart")}, map[string]interface{}{"confirmation": "terminate-and-restart"}), Response: acceptedResponse("结束任务已接收，当前 HTTP 连接将随后关闭")},
	{Name: "ocr_status", Method: http.MethodGet, Path: "/api/v1/ocr/status", Category: "ocr", Summary: "GLM-OCR 运行状态、队列与中文索引指标"},
	{Name: "ocr_config_get", Method: http.MethodGet, Path: "/api/v1/ocr/config", Category: "ocr", Summary: "读取 OCR 模式、实时开关、历史补录与监听范围"},
	{Name: "ocr_config_set", Method: http.MethodPut, Path: "/api/v1/ocr/config", Category: "ocr", Summary: "保存 OCR 模式、开关和监听范围并实时应用", SideEffect: true, RequestBody: ocrConfigRequestBody},
	{Name: "ocr_search", Method: http.MethodGet, Path: "/api/v1/ocr/search", Category: "ocr", Summary: "按 OCR 文字搜索图片消息", RequiresDatabase: true, Parameters: []APIParameter{query("keyword", "string", "", "图片内文字关键词", true), query("chats", "string", "", "逗号分隔会话", false), queryExample("time", "string", "", "时间范围", "last-30d", false), query("since", "string", "", "起始 Unix 时间", false), query("until", "string", "", "结束 Unix 时间", false), queryInt("limit", "20", "返回数量", false, number(1), number(500)), queryInt("offset", "0", "分页偏移", false, number(0), number(5000))}},
	{Name: "ocr_backfill", Method: http.MethodPost, Path: "/api/v1/ocr/backfill", Category: "ocr", Summary: "按监听范围把历史图片加入持久 OCR 队列", RequiresDatabase: true, SideEffect: true, Confirmation: true, RequestBody: objectRequestBody(nil, map[string]APIValueSchema{"limit": integerSchema("本次最多加入的图片数", number(1), number(10000)), "received_only": booleanSchema("只处理收到的图片"), "respect_scope": booleanSchema("仅处理当前监听范围")}, map[string]interface{}{"limit": 1000, "received_only": true, "respect_scope": true})},
	{Name: "ocr_backfill_stop", Method: http.MethodPost, Path: "/api/v1/ocr/backfill/stop", Category: "ocr", Summary: "停止正在扫描的历史图片补录任务", RequiresDatabase: true, SideEffect: true},
	{Name: "ocr_record_get", Method: http.MethodGet, Path: "/api/v1/ocr/index/{id}", Category: "ocr", Summary: "读取单条 OCR 的完整文字、Markdown 与版面结果", RequiresDatabase: true, Parameters: []APIParameter{pathParam("id", "OCR 索引记录 ID")}},
	{Name: "ocr_succeeded_clear", Method: http.MethodDelete, Path: "/api/v1/ocr/index/succeeded", Category: "ocr", Summary: "清空全部已识别 OCR 内容与搜索索引", RequiresDatabase: true, SideEffect: true, Confirmation: true},
	{Name: "ocr_waiting_media_retry_all", Method: http.MethodPost, Path: "/api/v1/ocr/index/waiting-media/retry", Category: "ocr", Summary: "批量向微信提交等待中图记录的原生下载请求并刷新接收 Hook", RequiresDatabase: true, SideEffect: true, RequestBody: objectRequestBody(nil, map[string]APIValueSchema{"limit": integerSchema("本次最多重试的等待中图记录数", number(1), number(ocrMediaRetryBatchLimit))}, map[string]interface{}{"limit": ocrMediaRetryBatchLimit})},
	{Name: "ocr_waiting_media_retry", Method: http.MethodPost, Path: "/api/v1/ocr/index/{id}/media-retry", Category: "ocr", Summary: "向微信提交单条等待中图记录的原生下载请求", RequiresDatabase: true, SideEffect: true, Parameters: []APIParameter{pathParam("id", "OCR 索引记录 ID")}},
	{Name: "ocr_retry", Method: http.MethodPost, Path: "/api/v1/ocr/index/{id}/retry", Category: "ocr", Summary: "立即重试失败的 OCR 记录", RequiresDatabase: true, SideEffect: true, Parameters: []APIParameter{pathParam("id", "OCR 索引记录 ID")}},
	{Name: "ocr_rerecognize", Method: http.MethodPost, Path: "/api/v1/ocr/index/{id}/rerecognize", Category: "ocr", Summary: "清除旧结果并重新识别已成功的 OCR 图片", RequiresDatabase: true, SideEffect: true, Confirmation: true, Parameters: []APIParameter{pathParam("id", "OCR 索引记录 ID")}},
	{Name: "ocr_local_service_start", Method: http.MethodPost, Path: "/api/v1/ocr/local-service/start", Category: "ocr", Summary: "手动启动本机 MLX 与 GLM-OCR SDK 服务", SideEffect: true},
	{Name: "ocr_local_service_stop", Method: http.MethodPost, Path: "/api/v1/ocr/local-service/stop", Category: "ocr", Summary: "手动停止本机模型服务并关闭异常自动重启", SideEffect: true, Confirmation: true},
	{Name: "api_meta", Method: http.MethodGet, Path: "/api/v1/meta", Category: "system", Summary: "机器可读的 HTTP API 目录与覆盖范围"},
	{Name: "openapi", Method: http.MethodGet, Path: "/api/v1/openapi.json", Category: "system", Summary: "OpenAPI 3.1 契约"},

	{Name: "hook_config_get", Method: http.MethodGet, Path: "/api/v1/hook/config", Category: "push", Summary: "读取推送规则"},
	{Name: "hook_config_set", Method: http.MethodPost, Path: "/api/v1/hook/config", Category: "push", Summary: "更新推送规则", RequestBody: hookConfigRequestBody},
	{Name: "hook_status", Method: http.MethodGet, Path: "/api/v1/hook/status", Category: "push", Summary: "读取推送运行状态"},
	{Name: "hook_events", Method: http.MethodGet, Path: "/api/v1/hook/events", Category: "push", Summary: "按需读取最近推送事件（实时更新由 SSE 提供）", Parameters: []APIParameter{queryInt("limit", "50", "返回事件数", false, number(1), number(200))}},
	{Name: "hook_events_clear", Method: http.MethodPost, Path: "/api/v1/hook/events/clear", Category: "push", Summary: "清空最近推送事件", Confirmation: true},
	{Name: "hook_events_batch_action", Method: http.MethodPost, Path: "/api/v1/hook/events/batch-action", Category: "push", Summary: "批量重试或删除持久投递记录", SideEffect: true, Confirmation: true, RequestBody: objectRequestBody([]string{"action", "event_ids"}, map[string]APIValueSchema{"action": stringSchema("操作", "retry", "delete"), "target": stringSchema("可选投递目标"), "event_ids": stringArraySchema("推送事件 ID，最多 200 条")}, map[string]interface{}{"action": "retry", "target": "", "event_ids": []string{"event-id"}})},
	{Name: "hook_event_action", Method: http.MethodPost, Path: "/api/v1/hook/events/{event_id}/action", Category: "push", Summary: "取消、重试或删除持久投递", SideEffect: true, Parameters: []APIParameter{pathParam("event_id", "推送事件 ID")}, RequestBody: objectRequestBody([]string{"action"}, map[string]APIValueSchema{"action": stringSchema("操作", "cancel", "retry", "delete"), "target": stringSchema("可选投递目标")}, map[string]interface{}{"action": "retry", "target": "post"})},
	{Name: "hook_hermes_weixin_get", Method: http.MethodGet, Path: "/api/v1/hook/hermes/weixin", Category: "push", Summary: "读取 Hermes 微信渠道配置"},
	{Name: "hook_hermes_weixin_set", Method: http.MethodPost, Path: "/api/v1/hook/hermes/weixin", Category: "push", Summary: "更新 Hermes 微信渠道配置", RequestBody: hermesWeixinRequestBody},
	{Name: "hook_hermes_qq_get", Method: http.MethodGet, Path: "/api/v1/hook/hermes/qq", Category: "push", Summary: "读取 Hermes QQ 渠道配置"},
	{Name: "hook_hermes_qq_set", Method: http.MethodPost, Path: "/api/v1/hook/hermes/qq", Category: "push", Summary: "更新 Hermes QQ 渠道配置", RequestBody: hermesQQRequestBody},

	{Name: "sessions", Method: http.MethodGet, Path: "/api/v1/sessions", Category: "chat", Summary: "最近会话", RequiresDatabase: true, Parameters: []APIParameter{query("query", "string", "", "会话关键词", false), queryInt("limit", "20", "返回数量", false, number(1), number(500)), queryInt("offset", "0", "跳过匹配会话数量", false, number(0), nil)}},
	{Name: "history", Method: http.MethodGet, Path: "/api/v1/history", Category: "chat", Summary: "聊天记录", RequiresDatabase: true, Parameters: []APIParameter{query("chat", "string", "", "会话名称或 ID", true), query("sender", "string", "", "群聊发送者", false), query("keyword", "string", "", "内容关键词（按字面量匹配）", false), queryExample("time", "string", "", "时间范围；支持 last-7d、last-30d、today 或日期区间", "last-30d", false), query("since", "string", "", "起始时间", false), query("until", "string", "", "结束时间", false), query("msg_type", "integer", "0", "消息类型", false), query("sub_type", "integer", "0", "消息子类型", false), queryInt("hour", "", "小时筛选", false, number(0), number(23)), query("is_self", "boolean", "", "是否本人发送", false), query("has_media", "boolean", "", "是否包含媒体", false), queryInt("limit", "50", "返回数量", false, number(1), nil), queryInt("offset", "0", "分页偏移", false, number(0), nil)}},
	{Name: "search", Method: http.MethodGet, Path: "/api/v1/search", Category: "chat", Summary: "全文搜索消息", RequiresDatabase: true, Parameters: []APIParameter{query("keyword", "string", "", "搜索词", true), query("chats", "string", "", "逗号分隔会话", false), queryExample("time", "string", "", "时间范围；支持 last-7d、last-30d、today 或日期区间", "last-30d", false), query("since", "string", "", "起始时间", false), query("until", "string", "", "结束时间", false), query("msg_type", "integer", "0", "消息类型", false), queryEnum("match", "phrase", "匹配模式", false, "phrase", "all", "any"), queryEnum("sort", "time", "排序", false, "time", "relevance"), query("cursor", "string", "", "稳定分页游标；使用后 offset 保持 0", false), queryInt("limit", "20", "返回数量", false, number(1), number(500)), queryInt("offset", "0", "分页偏移", false, number(0), number(5000))}},
	{Name: "unread", Method: http.MethodGet, Path: "/api/v1/unread", Category: "chat", Summary: "未读会话", RequiresDatabase: true, Parameters: []APIParameter{queryEnum("filter", "all", "会话类型", false, "all", "private", "group", "official_account", "folded"), queryInt("limit", "20", "返回数量", false, number(1), nil)}},
	{Name: "members", Method: http.MethodGet, Path: "/api/v1/members", Category: "chat", Summary: "群成员", RequiresDatabase: true, Parameters: []APIParameter{query("chat", "string", "", "群聊 ID", true)}},
	{Name: "new_messages", Method: http.MethodGet, Path: "/api/v1/new_messages", Category: "chat", Summary: "轮询增量读取新消息", RequiresDatabase: true, Parameters: []APIParameter{headerParam("X-Chatlog-State", "string", "JSON 编码字符串；原样回传上一次响应的 new_state，避免会话游标出现在 URL/日志中", false), query("state", "string", "", "兼容参数：JSON 编码字符串；原样回传上一次响应的 new_state；新客户端应使用 X-Chatlog-State 请求头", false), queryInt("limit", "200", "返回数量", false, number(1), number(5000))}},
	{Name: "stats", Method: http.MethodGet, Path: "/api/v1/stats", Category: "chat", Summary: "会话统计（带水位增量缓存）", RequiresDatabase: true, Parameters: []APIParameter{query("chat", "string", "", "会话名称或 ID", true), queryExample("time", "string", "", "时间范围；支持 last-7d、last-30d、today 或日期区间", "last-30d", false), query("since", "string", "", "起始时间", false), query("until", "string", "", "结束时间", false), query("force", "boolean", "false", "强制从普通消息表完整重算", false)}},
	{Name: "keyword_analytics", Method: http.MethodGet, Path: "/api/v1/analytics/keywords", Category: "chat", Summary: "关键词消息分析", RequiresDatabase: true, Parameters: []APIParameter{query("keyword", "string", "", "按字面量分析的关键词", true), query("chats", "string", "", "逗号分隔会话；留空分析全部会话", false), queryExample("time", "string", "", "时间范围；支持 last-7d、last-30d、today 或日期区间", "last-30d", false), query("since", "string", "", "起始时间", false), query("until", "string", "", "结束时间", false), query("msg_type", "integer", "0", "消息类型", false), queryInt("chat_limit", "20", "聊天对象排行数量", false, number(1), number(100)), query("force", "boolean", "false", "忽略短期分析缓存", false)}},
	{Name: "favorites", Method: http.MethodGet, Path: "/api/v1/favorites", Category: "social", Summary: "收藏", RequiresDatabase: true, Parameters: []APIParameter{query("fav_type", "integer", "0", "收藏类型", false), query("query", "string", "", "关键词", false), queryInt("limit", "50", "返回数量", false, number(1), nil)}},
	{Name: "sns_notifications", Method: http.MethodGet, Path: "/api/v1/sns_notifications", Category: "social", Summary: "朋友圈通知", RequiresDatabase: true, Parameters: []APIParameter{queryExample("time", "string", "", "时间范围；支持 last-7d、last-30d、today 或日期区间", "last-7d", false), query("since", "string", "", "起始时间", false), query("until", "string", "", "结束时间", false), query("include_read", "boolean", "false", "包含已读", false), queryInt("limit", "50", "返回数量", false, number(1), nil)}},
	{Name: "sns_feed", Method: http.MethodGet, Path: "/api/v1/sns_feed", Category: "social", Summary: "朋友圈动态", RequiresDatabase: true, Parameters: []APIParameter{query("user", "string", "", "发布者", false), queryExample("time", "string", "", "时间范围；支持 last-7d、last-30d、today 或日期区间", "last-30d", false), query("since", "string", "", "起始时间", false), query("until", "string", "", "结束时间", false), queryInt("limit", "20", "返回数量", false, number(1), nil)}},
	{Name: "sns_search", Method: http.MethodGet, Path: "/api/v1/sns_search", Category: "social", Summary: "搜索朋友圈", RequiresDatabase: true, Parameters: []APIParameter{query("keyword", "string", "", "搜索词", true), query("user", "string", "", "发布者", false), queryExample("time", "string", "", "时间范围；支持 last-7d、last-30d、today 或日期区间", "last-30d", false), query("since", "string", "", "起始时间", false), query("until", "string", "", "结束时间", false), queryInt("limit", "20", "返回数量", false, number(1), nil)}},
	{Name: "sns_media_proxy", Method: http.MethodGet, Path: "/api/v1/sns/media/proxy", Category: "media", Summary: "下载并解密朋友圈媒体", RequiresDatabase: true, Response: binaryResponse("朋友圈媒体文件", "application/octet-stream", "image/*", "video/*"), Parameters: []APIParameter{query("url", "string", "", "公网媒体 URL", true), query("key", "string", "", "媒体密钥", false)}},
	{Name: "contacts", Method: http.MethodGet, Path: "/api/v1/contacts", Category: "address_book", Summary: "联系人", RequiresDatabase: true, Parameters: []APIParameter{query("query", "string", "", "关键词", false), query("is_friend", "boolean", "", "好友筛选", false), queryInt("limit", "500", "返回数量", false, number(1), nil), queryInt("offset", "0", "分页偏移", false, number(0), nil)}},
	{Name: "openim_corp", Method: http.MethodGet, Path: "/api/v1/openim/{wxid}/corp", Category: "address_book", Summary: "企业微信联系人企业信息", RequiresDatabase: true, Parameters: []APIParameter{pathParam("wxid", "联系人 wxid")}},
	{Name: "chatrooms", Method: http.MethodGet, Path: "/api/v1/chatrooms", Category: "address_book", Summary: "群聊", RequiresDatabase: true, Parameters: []APIParameter{query("query", "string", "", "关键词", false), queryInt("limit", "500", "返回数量", false, number(1), nil), queryInt("offset", "0", "分页偏移", false, number(0), nil)}},

	{Name: "databases", Method: http.MethodGet, Path: "/api/v1/db", Category: "database", Summary: "数据库文件清单", RequiresDatabase: true},
	{Name: "database_modules", Method: http.MethodGet, Path: "/api/v1/db/modules", Category: "database", Summary: "数据库业务模块及能力状态", RequiresDatabase: true},
	{Name: "database_auxiliary", Method: http.MethodGet, Path: "/api/v1/db/auxiliary", Category: "database", Summary: "辅助数据库语义数据集", RequiresDatabase: true},
	{Name: "database_auxiliary_dataset", Method: http.MethodGet, Path: "/api/v1/db/auxiliary/{dataset}", Category: "database", Summary: "读取辅助数据库语义数据", RequiresDatabase: true, Parameters: []APIParameter{pathParam("dataset", "数据集 ID"), query("query", "string", "", "关键词", false), queryInt("limit", "50", "返回数量", false, number(1), number(500)), queryInt("offset", "0", "分页偏移", false, number(0), number(100000))}},
	{Name: "database_search", Method: http.MethodGet, Path: "/api/v1/db/search", Category: "database", Summary: "跨库搜索", RequiresDatabase: true, Parameters: []APIParameter{query("keyword", "string", "", "搜索词", true), queryEnum("mode", "quick", "搜索模式", false, "quick", "deep"), queryEnum("match", "phrase", "匹配模式", false, "phrase", "all", "any"), queryEnum("sort", "relevance", "排序", false, "relevance", "source"), query("group", "string", "", "逗号分隔数据库分组", false), query("file", "string", "", "逗号分隔文件名或完整路径", false), queryInt("limit", "100", "返回数量", false, number(1), number(500)), queryInt("offset", "0", "分页偏移", false, number(0), number(5000)), queryInt("timeout_ms", "15000", "搜索超时毫秒", false, number(100), number(60000))}},
	{Name: "database_search_consistency", Method: http.MethodGet, Path: "/api/v1/db/search/consistency", Category: "database", Summary: "搜索索引与业务数据一致性检查", RequiresDatabase: true, Parameters: []APIParameter{query("refresh", "boolean", "false", "重新执行数据库审计", false)}},
	{Name: "database_tables", Method: http.MethodGet, Path: "/api/v1/db/tables", Category: "database", Summary: "可浏览数据库表清单", RequiresDatabase: true, Parameters: []APIParameter{query("group", "string", "", "数据库组", true), query("file", "string", "", "数据库文件", true)}},
	{Name: "database_data", Method: http.MethodGet, Path: "/api/v1/db/data", Category: "database", Summary: "浏览数据库表", RequiresDatabase: true, Response: formatResponse("分页 JSON 表数据或完整流式导出文件"), Parameters: []APIParameter{query("group", "string", "", "数据库组", true), query("file", "string", "", "数据库文件", true), query("table", "string", "", "表名", true), query("keyword", "string", "", "行内搜索词", false), queryInt("limit", "20", "JSON 返回数量", false, number(1), number(10000)), queryInt("offset", "0", "JSON 分页偏移；导出格式忽略该值并输出完整结果", false, number(0), nil), queryEnum("format", "json", "响应格式", false, "json", "csv", "xlsx", "excel")}},
	{Name: "database_query", Method: http.MethodGet, Path: "/api/v1/db/query", Category: "database", Summary: "执行只读 SQL", RequiresDatabase: true, Response: formatResponse("分页 JSON 查询结果或完整流式导出文件"), Parameters: []APIParameter{query("group", "string", "", "数据库组", true), query("file", "string", "", "数据库文件", true), query("sql", "string", "", "SELECT/WITH/EXPLAIN/PRAGMA", true), queryInt("limit", "10000", "JSON 返回数量", false, number(1), number(10000)), queryInt("offset", "0", "JSON 分页偏移；导出格式忽略该值并输出完整结果", false, number(0), nil), queryEnum("format", "json", "响应格式", false, "json", "csv", "xlsx", "excel")}},
	{Name: "database_audit", Method: http.MethodGet, Path: "/api/v1/db/audit", Category: "database", Summary: "全库解析覆盖率与分片水位审计", RequiresDatabase: true, Parameters: []APIParameter{query("refresh", "boolean", "false", "忽略五分钟缓存并重新扫描", false)}},
	{Name: "database_message_resources", Method: http.MethodGet, Path: "/api/v1/db/message_resources", Category: "database", Summary: "查询消息与资源记录的关联", RequiresDatabase: true, Parameters: []APIParameter{query("message_id", "integer", "", "MessageResourceInfo.message_id", false), query("message_local_id", "integer", "", "消息分片 local_id", false), query("message_svr_id", "integer", "", "消息 server_id", false), query("chat_id", "integer", "", "资源库 chat_id", false), queryInt("limit", "100", "返回数量", false, number(1), number(500)), queryInt("offset", "0", "分页偏移", false, number(0), nil)}},
	{Name: "cache_status", Method: http.MethodGet, Path: "/api/v1/cache", Category: "system", Summary: "分类读取运行时与磁盘缓存详情"},
	{Name: "cache_files", Method: http.MethodGet, Path: "/api/v1/cache/files", Category: "system", Summary: "分页读取磁盘缓存文件路径", Parameters: []APIParameter{queryEnum("category", "", "磁盘缓存分类", true, "decoded_media", "sns_media", "sns_wasm", "wcdb", "push_media"), queryInt("limit", "500", "返回文件数", false, number(1), number(5000)), queryInt("offset", "0", "分页偏移", false, number(0), number(100000))}},
	{Name: "cache_clear", Method: http.MethodPost, Path: "/api/v1/cache/clear", Category: "system", Summary: "按分类清理运行时与磁盘缓存", SideEffect: true, Confirmation: true, Parameters: []APIParameter{queryEnum("category", "all", "缓存分类", false, "all", "media_paths", "sns_keys", "statistics", "decoded_media", "sns_media", "sns_wasm", "wcdb", "push_media")}},
	{Name: "cache_open", Method: http.MethodPost, Path: "/api/v1/cache/open", Category: "system", Summary: "在系统文件管理器打开缓存路径", SideEffect: true, Parameters: []APIParameter{queryEnum("category", "", "磁盘缓存分类", true, "decoded_media", "sns_media", "sns_wasm", "wcdb", "push_media"), query("path", "string", "", "分类内的文件或目录绝对路径", false)}},

	{Name: "image", Method: http.MethodGet, Path: "/image/{key}", Category: "media", Summary: "读取图片", Response: mediaResponse("图片或媒体元数据"), Parameters: []APIParameter{pathParam("key", "媒体 key"), queryEnum("info", "", "只返回媒体元数据", false, "1")}},
	{Name: "video", Method: http.MethodGet, Path: "/video/{key}", Category: "media", Summary: "读取视频", Response: mediaResponse("视频或媒体元数据"), Parameters: []APIParameter{pathParam("key", "媒体 key"), queryEnum("info", "", "只返回媒体元数据", false, "1")}},
	{Name: "file", Method: http.MethodGet, Path: "/file/{key}", Category: "media", Summary: "读取附件", Response: mediaResponse("附件或媒体元数据"), Parameters: []APIParameter{pathParam("key", "媒体 key"), queryEnum("info", "", "只返回媒体元数据", false, "1")}},
	{Name: "voice", Method: http.MethodGet, Path: "/voice/{key}", Category: "media", Summary: "读取语音", Response: mediaResponse("语音或媒体元数据"), Parameters: []APIParameter{pathParam("key", "媒体 key"), queryEnum("info", "", "只返回媒体元数据", false, "1")}},
	{Name: "data", Method: http.MethodGet, Path: "/data/{path}", Category: "media", Summary: "读取账号数据目录中的文件", Response: binaryResponse("账号数据文件", "application/octet-stream"), Parameters: []APIParameter{pathParam("path", "数据目录相对路径")}},
}

// EndpointCatalog returns a copy sorted by stable endpoint name.
func EndpointCatalog() []EndpointSpec {
	items := make([]EndpointSpec, len(endpointCatalog))
	for i, endpoint := range endpointCatalog {
		items[i] = normalizeEndpoint(endpoint)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func normalizeEndpoint(endpoint EndpointSpec) EndpointSpec {
	endpoint.Parameters = append([]APIParameter(nil), endpoint.Parameters...)
	for i := range endpoint.Parameters {
		endpoint.Parameters[i].Enum = append([]string(nil), endpoint.Parameters[i].Enum...)
	}
	if endpoint.Response.Mode == "" {
		endpoint.Response = jsonResponse("JSON response")
	} else {
		endpoint.Response.ContentTypes = append([]string(nil), endpoint.Response.ContentTypes...)
		endpoint.Response.BinaryValues = append([]string(nil), endpoint.Response.BinaryValues...)
	}
	if endpoint.RequestBody != nil {
		requestBody := *endpoint.RequestBody
		requestBody.Schema = cloneAPIValueSchema(endpoint.RequestBody.Schema)
		endpoint.RequestBody = &requestBody
	}
	if endpoint.Method != http.MethodGet && endpoint.Method != http.MethodHead {
		endpoint.SideEffect = true
	}
	return endpoint
}

func cloneAPIValueSchema(schema APIValueSchema) APIValueSchema {
	schema.Enum = append([]string(nil), schema.Enum...)
	schema.Required = append([]string(nil), schema.Required...)
	if schema.Properties != nil {
		properties := make(map[string]APIValueSchema, len(schema.Properties))
		for name, property := range schema.Properties {
			properties[name] = cloneAPIValueSchema(property)
		}
		schema.Properties = properties
	}
	if schema.Items != nil {
		item := cloneAPIValueSchema(*schema.Items)
		schema.Items = &item
	}
	schema.Example = cloneJSONValue(schema.Example)
	return schema
}

func cloneJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			result[key] = cloneJSONValue(item)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = cloneJSONValue(item)
		}
		return result
	default:
		return value
	}
}

// FindEndpoint resolves a case-insensitive CLI alias.
func FindEndpoint(name string) (EndpointSpec, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, endpoint := range endpointCatalog {
		if strings.ToLower(endpoint.Name) == name {
			return normalizeEndpoint(endpoint), true
		}
	}
	return EndpointSpec{}, false
}

func (s *Service) handleAPICatalog(c *gin.Context) {
	items := EndpointCatalog()
	c.JSON(http.StatusOK, gin.H{
		"version":          "v1",
		"contract_version": "2.0",
		"transport":        "HTTP JSON; event updates use bounded polling",
		"openapi_url":      "/api/v1/openapi.json",
		"total":            len(items),
		"endpoints":        items,
		"client_features": []string{
			"endpoint describe",
			"required/type/enum/range validation",
			"structured request body validation",
			"response mode and output validation",
			"OpenAPI 3.1 generation",
		},
		"coverage": gin.H{
			"data_query":        "complete",
			"push_control":      "complete",
			"media":             "complete",
			"process_operation": "CLI only: chatlog ops",
		},
		"http_exclusions": []string{
			"account selection",
			"key extraction",
			"database decryption",
			"HTTP service lifecycle",
			"persistent runtime configuration",
		},
	})
}
