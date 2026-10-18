# GLM-OCR 图片识别、索引与文搜图

## 架构选择

默认使用官方推荐的完整自托管链路，而不是只把整图直接交给模型：

```text
收到的图片
  -> Chatlog 媒体定位与 DAT 解密
  -> GLM-OCR SDK Server
       -> PP-DocLayout-V3 版面分析
       -> vLLM / GLM-OCR 并行区域识别
       -> Markdown + JSON layout
  -> Chatlog 独立 SQLite sidecar
       -> FTS5 trigram 中文子串索引
       -> 短关键词 / 无 FTS5 时 literal fallback
  -> 文搜图 + 图片描述 + 图片预览
```

WeChat 原始数据库保持只读。索引默认位于：

```text
<work_dir>/ocr/image_ocr.db
```

索引使用 WAL、幂等消息/媒体键、持久待处理队列、失败退避和手动重试。
FTS5 可用时采用 `tokenize='trigram'`，适合无需分词词典的中文子串搜索；
一个或两个字的关键词自动使用转义后的字面量匹配。

## 方式二：本地自托管（默认）

### Apple Silicon Mac 自动启动

Apple Silicon 使用官方推荐的 MLX 双环境方案。开启本地 OCR 后，Chatlog
检查 `127.0.0.1:8081/health`；服务未运行时自动执行
`deploy/ocr/start-macos-mlx.sh`：

1. 首次创建隔离的 MLX 与 SDK Python 3.12 环境；
2. 在 `8080` 启动 `mlx-vlm` Metal 推理服务；
3. 在 `8081` 启动 GLM-OCR SDK 布局分析服务；
4. 服务预热完成后继续处理持久队列；
5. 先前因 `connection refused` 失败的记录自动重新排队。

MLX 推理后端和 GLM-OCR SDK 由同一个生命周期管理器控制：启动时按依赖
顺序拉起并以“两端均就绪”作为成功条件，任一端失败会停止整个进程组；停止
时同时发送退出信号并等待两个主进程及其子进程结束。页面出现单端运行时，
“同步修复模型服务”会重新收敛为双运行或双停止状态。

OCR 页面展示安装、MLX 启动、SDK 启动、模型加载、运行或错误状态，以及
`~/.chatlog/ocr-runtime/logs` 日志目录。

```bash
export CHATLOG_OCR_LOCAL_AUTO_START=true
export CHATLOG_OCR_RUNTIME_DIR="$HOME/.chatlog/ocr-runtime"
# 可选：指定另一份启动脚本
export CHATLOG_OCR_LOCAL_START_SCRIPT="/path/to/start-macos-mlx.sh"
```

### NVIDIA GPU / Docker

GPU 主机直接使用仓库内的部署栈：

```bash
cd deploy/ocr
docker compose -f docker-compose.vllm.yml up -d --build
curl --fail http://127.0.0.1:8081/health
```

Chatlog 配置：

```bash
export CHATLOG_OCR_ENABLED=true
export CHATLOG_OCR_PROVIDER=vllm
export CHATLOG_OCR_ENDPOINT=http://127.0.0.1:8081/glmocr/parse
export CHATLOG_OCR_MODEL=glm-ocr
export CHATLOG_OCR_RECEIVED_ONLY=true
# 留空表示监听全部；也可按逗号指定联系人和群聊
export CHATLOG_OCR_LISTEN_CONTACTS=wxid_a,wxid_b
export CHATLOG_OCR_LISTEN_CHATROOMS=123456789@chatroom
```

该部署固定使用官方 README 对应的 vLLM 版本、`transformers>=5.3.0`
兼容的 SDK，以及 MTP speculative decoding。完整参数见
`deploy/ocr/docker-compose.vllm.yml`。

## 方式一：智谱 MaaS

MaaS 与自托管使用同一持久队列、描述字段和索引，只切换调用端：

```bash
export CHATLOG_OCR_ENABLED=true
export CHATLOG_OCR_PROVIDER=maas
export CHATLOG_OCR_ENDPOINT=https://open.bigmodel.cn/api/paas/v4/layout_parsing
read -rsp 'Zhipu API key: ' CHATLOG_OCR_API_KEY
export CHATLOG_OCR_API_KEY
```

API key 不应写入仓库、命令参数或公开日志。服务配置文件和 OCR SQLite
sidecar 均使用仅当前用户可读的权限。

## 增量与历史图片

- Web 控制台提供独立的「OCR 识别」子页面，可分别开启实时 OCR 与历史补录。
- 实时 OCR 每次开启都会建立新的激活水位，只处理开启之后收到的新图片。
- `received_only=true`：忽略自己发送的图片。
- 监听范围为空时覆盖全部会话；选择范围后只扫描指定联系人和群聊。
- 需要历史识别时，在 Web「OCR 识别 -> 历史图片补录」开启补录并启动任务，
  或调用：

```bash
chatlog api call ocr_backfill --confirm \
  --body '{"limit":1000,"received_only":true,"respect_scope":true}'
```

## HTTP API

```text
GET  /api/v1/ocr/status
GET  /api/v1/ocr/config
PUT  /api/v1/ocr/config
GET  /api/v1/ocr/search?keyword=关键词&chats=wxid_xxx&limit=50
POST /api/v1/ocr/backfill
POST /api/v1/ocr/backfill/stop
POST /api/v1/ocr/index/{id}/retry
```

图片消息 `contents` 新增：

```json
{
  "image_description": "图片包含标题、正文；识别文字：...",
  "ocr_text": "完整可搜索文本",
  "ocr_markdown": "完整 Markdown",
  "ocr_layout": [],
  "ocr_provider": "vllm",
  "ocr_model": "glm-ocr",
  "ocr_status": "succeeded",
  "ocr_index_id": 123
}
```

Web「OCR 识别」页面显示实时/补录开关、本地/API 模式、联系人和群聊选择器、
队列与处理指标、最近任务、错误、重试时间和索引详情。消息卡片在图片下方显示
`image_description`，文搜图结果沿用相同聊天预览组件，因此会直接关联
`/image/{media_key}` 并显示图片与描述。
