# Chatlog LLM CLI 调用手册

本文是给 LLM、Agent、脚本与外部编排器读取的稳定调用契约。

## 1. 规则

1. 初始化前先执行 `chatlog ops accounts`。
2. 先执行 `chatlog api catalog --compact` 选择入口，再执行 `chatlog api describe <endpoint>` 获取完整契约；不要猜 endpoint、method、path 或字段。
3. 只解析 stdout：`api` 输出单个 JSON 对象，`ops` 输出 JSON Lines。
4. 每次调用检查 `ok`、进程 exit code；长任务同时检查最终 `type=success|error`。
5. 不把密钥、Token、Secret 写入 prompt、日志或命令历史。需要写配置时优先使用权限受控的文件。
6. 根据 `response.mode` 决定输出：`binary` 必须使用 `--output`；`media` 在未传 `info=1` 时必须使用；`format` 在 `csv/xlsx/excel` 时必须使用。
7. 默认地址为 `127.0.0.1:5030`；不要将服务暴露到非 loopback 地址。
8. 时间范围使用 `last-7d`、`last-30d`、`last-3m`、`today` 或明确日期区间；不要猜成 `7d`/`30d`。

## 2. 命令结构

```text
chatlog
├── ops
│   ├── accounts
│   ├── status
│   ├── account-select
│   ├── database-key
│   ├── image-key
│   ├── server-start
│   └── config-set
└── api
    ├── catalog
    ├── describe <endpoint>
    ├── openapi
    └── call <endpoint>
```

无子命令的 `chatlog` 启动人工 TUI，不用于无人值守调用。

## 3. 运维接口 `chatlog ops`

### 账号发现与选择

```bash
chatlog ops accounts
chatlog ops account-select --pid 12345
chatlog ops account-select --account wxid_example
chatlog ops status --account wxid_example
```

除 `accounts`、`config-set` 外，目标命令支持：

```text
--pid <wechat-pid>
--account <history-account-name>
```

### 密钥与数据库

```bash
chatlog ops database-key --account wxid_example
chatlog ops image-key --account wxid_example
```

`database-key` 可能重启微信并等待 Frida 捕获。数据库查询通过只读 VFS 直接读取加密 WCDB，页面按需在内存解密，已提交 WAL 也只在内存合并。

### 服务与配置

```bash
chatlog ops config-set --http-addr 127.0.0.1:5030
chatlog ops config-set --work-dir /path/to/work
chatlog ops server-start --account wxid_example
```

可配置字段：

```text
--http-addr
--work-dir
--data-key
--image-key
--data-dir
```

不要在共享 shell history 中直接传 `--data-key`、`--image-key`。

### JSON Lines 事件

```json
{"type":"action_started","action":"server-start","stage":"starting","message":"正在启动 HTTP 服务","timestamp":"2026-07-23T12:00:00+08:00"}
{"type":"success","action":"server-start","stage":"running","message":"HTTP 服务已启动","timestamp":"2026-07-23T12:00:01+08:00"}
```

## 4. API 契约发现

### 低 token 目录

```bash
chatlog api catalog --compact
chatlog api catalog --category database --compact
```

### 单 endpoint 描述

```bash
chatlog api describe history
chatlog api describe hook_config_set
```

描述包含：

- method、path、category、side effect、local-only、确认要求；
- query/path 参数的 required、type、default、enum、minimum、maximum；
- POST 请求体 JSON Schema、required fields 和 example；
- 响应 `mode`、content types、success status 和动态格式控制字段。

示例：

```json
{
  "ok": true,
  "endpoint": {
    "name": "history",
    "method": "GET",
    "path": "/api/v1/history",
    "parameters": [
      {"name":"chat","in":"query","type":"string","required":true},
      {"name":"limit","in":"query","type":"integer","default":"50","minimum":1}
    ],
    "response": {"mode":"json","content_types":["application/json"],"success_status":200}
  }
}
```

### OpenAPI 3.1

```bash
chatlog api openapi > openapi.json
curl -s http://127.0.0.1:5030/api/v1/openapi.json > openapi.json
```

目录、OpenAPI、服务端路由和 CLI 共用一份 Go 契约。测试会比对全部 **39 个 endpoint**。

## 5. API 调用 `chatlog api call`

通用语法：

```bash
chatlog api call <endpoint> \
  --addr 127.0.0.1:5030 \
  --timeout 30 \
  --param key=value \
  --path-param key=value \
  --header key=value \
  --body '{"key":"value"}' \
  --confirm \
  --output ./file.bin
```

参数：

| 参数 | 用途 |
| --- | --- |
| `--param/-p key=value` | query 参数，可重复，值自动 URL encode |
| `--path-param key=value` | 填充目录 path 中的 `{name}`，可重复 |
| `--header key=value` | 自定义 header，可重复 |
| `--body '<json>'` | JSON body |
| `--body-file <path>` | 从文件读取 JSON body；与 `--body` 互斥 |
| `--confirm` | 执行 `confirmation_required=true` 的副作用操作 |
| `--output/-o <path>` | 保存媒体或其他二进制响应 |
| `--timeout <seconds>` | 超时；`0` 表示不限制 |

CLI 会在网络请求前拒绝：未知参数、缺少必填参数、类型错误、非法 enum、越界数值、未知 JSON body 字段以及缺少 `--output`。失败仍输出 JSON 且 exit code 非零。

### 查询示例

```bash
chatlog api call sessions --param query=项目 --param limit=50
chatlog api call history --param chat=filehelper --param time=last-30d --param limit=100 --param offset=0
chatlog api call search --param keyword=报价 --param chats=工作群 --param limit=20
chatlog api call contacts --param is_friend=true --param limit=100
chatlog api call database_search --param keyword=项目 --param mode=deep
chatlog api call database_query \
  --param group=message \
  --param file=message_0.db \
  --param 'sql=select count(*) as count from MSG'
```

`history` 的 `time` 可省略，关键词按字面量匹配。消息项中的 `type` 是可读类型名，`type_num`/`sub_type` 是数据库解析后的数值；`message_id` 与兼容字段 `local_id` 是稳定消息 ID，`db_local_id` 才是原表 `local_id`。

`database_tables` 只返回当前驱动可直接查询的表，不返回会因未加载 SQLite 扩展而必然失败的虚表入口。`database_search.mode` 只能是 `quick` 或 `deep`，数据库查询中的非法 `limit`、`offset`、`format` 会直接失败，不会静默降级。

`database_query` 的 JSON 响应每页最多 10000 行；响应 envelope 的 `pagination.has_more` 和 `pagination.next_offset` 用于继续读取后续页。`database_data` 同样使用 `limit`/`offset` 分页。两者在 `format=csv|xlsx|excel` 时会忽略分页参数并流式导出完整结果；CSV 边查询边发送，XLSX 使用流式工作表并在超过单表行数时自动续到下一张工作表。

增量轮询必须原样回传游标，不要解析或自行计算 `new_state`：

```bash
chatlog api call new_messages --param limit=100
chatlog api call new_messages --header 'X-Chatlog-State=<上一响应 data.new_state 的紧凑 JSON>' --param limit=100
```

`has_more=true` 时继续携带最新 `new_state` 拉取；`partial_errors>0` 时可重试，同一批未成功读取的会话不会被游标跳过。首次不传状态时只回溯最近 24 小时。`state` 查询参数仍兼容旧客户端，但新调用应使用 `X-Chatlog-State`，避免会话游标出现在 URL 和访问日志中。

### POST 示例

```bash
chatlog api call cache_clear --confirm
chatlog api call hook_config_set --body-file ./hook-config.json
chatlog api call hook_events_clear --confirm
```

Hermes QQ 更新请求体与 Handler 使用同一组字段：

```bash
chatlog api call hook_hermes_qq_set --body '{
  "hermes_home":"/path/to/.hermes",
  "app_id":"app-id",
  "client_secret":"secret",
  "home_channel":"group:group_openid",
  "home_channel_name":"告警群"
}'
```

不存在 `bot_qq`、`sandbox`、`home_guild_id`、`home_channel_id` 这些旧字段；CLI 会在联网前拒绝它们。

### Path 与媒体示例

```bash
chatlog api call openim_corp --path-param wxid=wxid_example
chatlog api call image --path-param key=<media-key> --output ./image.jpg
chatlog api call data --path-param path=relative/path/file --output ./file.bin
chatlog api call image --path-param key=<media-key> --param info=1
chatlog api call database_data --param group=message --param file=message_0.db --param table=MSG --param format=xlsx --output ./MSG.xlsx
```

## 6. 响应 envelope

成功：

```json
{
  "ok": true,
  "endpoint": "sessions",
  "method": "GET",
  "url": "http://127.0.0.1:5030/api/v1/sessions?limit=50",
  "status": 200,
  "content_type": "application/json",
  "data": {"sessions": []}
}
```

二进制保存：

```json
{
  "ok": true,
  "endpoint": "image",
  "method": "GET",
  "status": 200,
  "content_type": "image/jpeg",
  "saved_to": "/absolute/path/image.jpg",
  "bytes": 12345
}
```

失败时 stdout 仍为 JSON，进程返回非零：

```json
{"ok":false,"endpoint":"history","status":400,"error":"Invalid Argument: chat"}
```

直接调用 HTTP API 时，所有错误统一使用对象 envelope：

```json
{"error":"invalid argument: chat"}
```

## 7. 推荐 Agent 流程

```text
1. chatlog ops accounts
2. chatlog ops status --account <selected>
3. 缺密钥 -> database-key / image-key
4. chatlog ops server-start --account <selected>   # 长驻子进程，直接读取原数据库
5. chatlog api catalog --compact
6. chatlog api describe <endpoint>
7. chatlog api call ping
8. 按 describe 契约调用
```

重试约束：

- `connection refused`：服务未启动，检查 `ops server-start`。
- HTTP `503`：数据库直读尚未就绪，检查 data dir/data key 后短暂退避。
- HTTP `400`：参数错误，不要原样重试；读取 `error` 和 catalog。
- HTTP `5xx`：最多指数退避重试 3 次，并保留脱敏错误。
- 密钥提取、配置写入等有副作用操作不得自动无限重试。
