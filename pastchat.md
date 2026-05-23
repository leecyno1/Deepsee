# 项目对话与改动总览（Past Chat Digest）

更新时间：2025-12-29

> 目的：把本轮（0913-2）围绕“AI 总结/会议路演/新闻舆情/联系人与黑白名单/缓存与 Token”多轮沟通的需求、关键决策、落地结果与验证点，整理成可持续迭代的摘要。  
> 说明：这里是“对话要点 + 结果落点”，不是逐字聊天记录。

---

## 1) 本轮对话需求（按用户原话归类）

### 会议路演信息表格（最核心）
- 表格排版错：表头错位、行高过大、留白过多、列宽不合理。
- 不允许横向拖动/横向滚动：所有内容需在文本框内完整展示；允许纵向滚动。
- 列宽强约束：时间/平台/会议号要显著缩窄；主讲人列最终要求删除；主题要点列保留并自动换行。
- 主题要点来源：必须从微信/邮件消息的“摘要 summary”读取，禁止拷贝原消息正文（减少冗余与 Token）。

### 新闻舆情“来源气泡”/徽标
- AI 总结里引用来源的徽标/气泡不显示（需要可悬停/点击查看来源详情）。

### 联系人管理评分/顶踩
- 联系人管理无法打分/评分不能输入保存。
- 勾选“顶/踩”后联系人评分不能加减分。
- 当评分 < 40 时自动加入黑名单，并在设置页提供候选下拉（含群聊）。

### 黑白名单（含群聊）要落到实处
- 联系人加入黑名单后，在“功能设置→黑名单管理”里看不到，需要梳理链路并让配置真正持久化。
- 黑名单的联系人/群聊对话不允许进入微信消息/邮件消息列表；需在“垃圾筛选”中统一剔除。

### AI 总结缓存与 Token 消耗
- 刷新页面后保留上次生成结果（持久化缓存）。
- 自定义提示词要能落到后端配置并在实际生成中生效（怀疑被忽略）。
- Token 消耗太快：希望增量更新、精简底层 JSON（剔除邮件地址/主题/附件链接、微信链接/图片等垃圾信息）。

### 其他 UI/交互
- 高级选项不再折叠：直接显示在按钮右侧。
- 经常无端切到邮件消息页面：修复自动切页。
- 微信“点击拉取后筛选列恢复默认”。
- 邮件列表不应自动更新，仅手动同步后更新。
- 近 1 天/分析模块/按钮/已恢复上次生成结果放同一行，整体更紧凑。

---

## 2) 已落地的关键结果（按模块）

### A. 会议路演表格（会议信息）
- 表格统一为 3 列：`时间｜平台/会议号｜主题要点`，删除“主讲人”列。
- 表格布局收紧：固定表格布局 `table-layout: fixed`，行高/内边距收敛到一行字符级别（非主题列）。
- 表头错位处理：在摘要卡片中覆盖全局 sticky 规则，使用 `top: -22px` 将表头向上校正。
- 横向滚动禁用：`overflow-x: hidden`；仅允许纵向滚动。
- 列宽定宽：时间/平台列为定宽（当前实现约 `70px / 110px`），空白显著减少；主题列自适应。
- 主题要点换行：主题列允许 `white-space: normal` 自动换行，尽量在同一文本框内展示完整内容。
- 数据来源约束：会议“主题要点”严格来自消息 `summary`（并做 `ai:` 前缀清理），不再复制原文长内容。

### B. 新闻舆情“来源气泡”
- AI 总结内容中对 `#id` 引用的解析增强：区分“消息引用/新闻引用”，支持悬停/点击弹出来源详情。
- 新增后端接口：`GET /api/newsfeed/by-ids?ids=...` 用于按 id 拉取新闻信息，供气泡展示。

### C. 联系人管理评分 + 顶/踩
- 联系人评分单元格支持手动输入（数字）并提供“保存”按钮提交。
- 微信消息行内“顶/踩”勾选可直连后端评分接口实现加减分（修复勾选后无法加减分）。
- 自动拉黑：评分 < 40 自动写入黑名单，并立即在列表隐藏其消息（黑名单强约束优先于 UI 筛选状态）。

### D. 黑白名单（含群聊）全链路
- 设置页可视化展示黑名单/白名单（联系人 + 群聊 talkers），支持移除。
- 提供候选下拉：从联系人与群聊候选接口拉取，支持把某个群聊直接加入黑名单。
- 强制执行策略：黑名单 senders/talkers 的消息不进入微信/邮件列表（与筛选控件无关）。

### E. AI 总结缓存与 Token 控制
- 增量摘要缓存：同一快照 + 模块 + 提示词 + 温度命中缓存直接返回，避免重复调用大模型。
- 缓存持久化：除进程内缓存外，将摘要缓存写入 DB（SyncState），刷新页面可恢复上次结果。
- Payload 瘦身：生成总结时仅传“必要字段（摘要/少量元数据）”，尽量不传原文/附件/URL 等冗余字段以降 Token。
- 提示词生效链路：运行时优先使用前端传入的提示词（用于验证自定义配置是否影响实际输出），并与后端配置合并。

### F. UI/交互修复
- 高级选项常驻展示：不再折叠；与主按钮同排。
- 修复无端切到邮件页：移除首开自动同步/自动切页逻辑。
- 微信“拉取后筛选复位”：拉取/同步后将筛选 UI 重置为默认，避免“拉取了但列表空”的错觉。
- 邮件列表更新策略：不自动刷新，改为用户手动同步后更新列表。
- 工具栏压缩：把“近 1 天 + 分析模块 + 按钮 + 状态（已恢复上次结果）”尽量放同一行，空间不足时允许换行。

---

## 3) 关键落点（接口/数据/文件）

### 前端（单页）
- `static/index.html`：会议路演表格 CSS 与渲染逻辑；新闻来源气泡；联系人评分输入与保存；黑白名单面板与候选；工具栏布局与紧凑样式；微信/邮件筛选逻辑修复。

### 后端（FastAPI）
- `app/routers/ai.py`：摘要生成链路、提示词合并、payload 瘦身、进程内 + SyncState 持久化缓存。
- `app/routers/news.py`：`/api/newsfeed/by-ids`。
- `app/routers/configs.py`：黑白名单读写（SyncState）。
- `app/main.py`：中间件（包含 GZip 压缩以改善首页加载体验）。

### 持久化（SyncState / DB）
- 摘要缓存键：`summary_cache:<snapshot_id>:<module>:<prompt_hash>:<temp>`
- 黑白名单：`blacklist_senders / blacklist_talkers / whitelist_senders / whitelist_talkers`（按实际存储键为准）

---

## 4) 建议验证清单（手工）
- 后端健康：`curl http://127.0.0.1:8001/api/health`
- 会议路演：AI 总结里会议信息是否 3 列；时间/平台列是否收窄；主题列自动换行；无横向滚动；表头不再错位。
- 新闻气泡：在 AI 总结中悬停/点击 `#...` 是否弹出来源详情；`curl 'http://127.0.0.1:8001/api/newsfeed/by-ids?ids=1'` 返回是否正常。
- 联系人评分：联系人管理输入分数点“保存”是否生效；微信消息行“顶/踩”是否能加减分；评分 < 40 是否自动拉黑并隐藏消息。
- 黑名单联动：在设置页能否看到刚加入的黑名单联系人/群聊；黑名单对象的消息/邮件是否不会出现在列表中。
- 微信拉取：点击拉取后筛选是否恢复默认展示。
- 邮件：是否仅手动同步后更新；是否不会无端切到邮件页。
- 刷新页面：AI 总结是否恢复上次生成结果（命中持久化缓存）。

---

## 5) 遗留/后续可迭代（来自对话的延伸点）
- 邮件黑名单匹配：建议对 `Name <a@b.com>` 做邮箱提取归一化，以提高命中率。
- 缓存治理：可加“清空摘要缓存”按钮/接口，定期清理 `summary_cache:` 前缀键，避免长期增长。
- 自动拉黑阈值：可在设置中参数化（默认 40），支持关闭/调整阈值。

---

## 6) 备份与发布记录（v0.8.0）

### GitHub 仓库（新建）
- Repo：`https://github.com/leecyno1/Deepsee`（Public）
- Release Tag：`v0.8.0`
- Release Commit：`653724debf4a738730368106d939dcaf072ce8b0`

### 本地备份（目录：`/Users/lichengyin/Desktop/Projects/0913_backups`）
- 预推送快照（含未提交改动）：`0913-prepush-20251222-105537.tar.gz`
  - sha256：`faac06325d66cdabb72488df04e4b861c8039e6dbe75aeaa556ffed970b42eef`
- 发布后备份（可恢复整个 git 历史）：`0913-v0.8.0-20251222-110010.bundle`
  - sha256：`1b452d0d9aaa2eb1c38656a5e3af6a36f5dd964deefab3356ecff1ed9f42c5b1`
- 发布源代码归档（基于 tag）：`0913-v0.8.0-20251222-110010.src.tar.gz`
  - sha256：`d673fac002a0e77d84a928ea76fc68daa41299435c5fefb27e759f78edc5d72e`

---

## 7) 0913-3：引入 `MediaCrawlerPro-Python` + `we-mp-rss`（自媒体/公众号/纪要聚合）

更新时间：2025-12-25

### 需求落地
- 新增 3 个聚合模块（与“纪要聚合/自媒体聚合”平行）：
  - 自媒体聚合：对接 `../MediaCrawlerPro-Python/data/results/*.json`
  - 纪要聚合：初版对接 `../MediaCrawlerPro-Python/data/meeting_records/*.json`（含音频下载；**已于 2025-12-26 改为本地录音/本地文件**）
  - 公众号聚合：对接 `../we-mp-rss/data/db.db`（articles + feeds + article_insights.summary）
- AI 总结页新增 3 个分析模块：`mediawatch / mpwatch / minuteswatch`，默认以“紧凑表格 + 既有摘要”输出，避免拷贝原文导致冗余与高 Token。

### 后端（新增/变更）
- 新增读取服务：
  - `app/services/media_store.py`：读取 MediaCrawlerPro 落盘 JSON（results + meeting_records）
  - `app/services/mp_rss_store.py`：读取 we-mp-rss SQLite（articles + insights）
- 新增 API：
  - `app/routers/media.py`：`/api/media/items`、`/api/media/meeting-records`、`/api/media/meeting/audio/{id}`
  - `app/routers/mp_rss.py`：`/api/mp/articles`、`/api/mp/articles/{id}?include_content=true`
  - `app/routers/tasks.py`：`/api/tasks`、`/api/tasks/{id}`（便于排查任务状态/结果）
- `app/routers/minutes.py`：已于 2025-12-26 调整为仅扫描本地 `data/minutes` + `data/recordings`（纪要聚合不再依赖 Media）。
- `app/routers/ai.py`：
  - 新增 3 个模块到模块列表与默认 prompts。
  - 外部聚合模块默认走本地摘要（表格化），避免远程大模型调用导致超时/高 Token；缓存 key 纳入数据源版本（mtime）。
  - `snapshot_service` 邮件/新闻快照字段进一步瘦身；快照内新闻抓取默认关闭（避免 Playwright 回退拖慢 summary）。

### 前端（单页）
- `static/index.html`：
  - 新增“公众号聚合”tab；“自媒体聚合”改为展示 MediaCrawlerPro 数据；“纪要聚合”支持 media meeting_records 音频链接。
  - AI 总结新增 3 个模块卡片并支持勾选参与总结。

### 追加修复（2025-12-25）
- `.env` 已写入 `MEDIA_SERVER_BASE=http://127.0.0.1:8001`；并在 `app/config.py` 增加 `MEDIA_SERVER_BASE` 配置项，避免 Pydantic “extra not permitted” 导致启动失败。
- `app/routers/media.py` 代理改为读取 `settings.MEDIA_SERVER_BASE`（确保 `.env` 生效）；**后续纪要聚合已去 Media 依赖，该按钮逻辑废弃**。
- `static/index.html` 的表格单元格弹窗改为优先展示“被点击的单元格”完整内容（不再误用整行 `.content` 字段）。
- `app/services/media_store.py` 修正缩进，避免后续维护/改动时引入逻辑误判。

---

## 8) 0913-4：表头错位与品牌回归修复

更新时间：2025-12-26

### 问题现象
- 多个模块表格出现“表头部分列下沉/错位”（通常从第 3～5 列开始），滚动时更明显。
- 顶部品牌区域 Dr.Lemon 标识不明显（用户感知为 logo 消失）。

### 修复落地
- `static/index.html`：
  - 修复根因：不再把 `#messageTable/#emailTable` 的 `thead th` 覆盖为 `position: static`，避免 sticky 失效导致的表头列错位。
  - 统一 `thead th` 的 `top` 偏移为 `top: var(--filters-height, 34px)`，保证表头始终贴合筛选条下方。
  - 顶部品牌：恢复 “Dr.Lemon + 柠檬 Logo + 信息聚合AI系统” 的紧凑展示，并补齐 `brand-name/brand-sub` 样式，确保浅色/深色都清晰可见。

### 验证
- 服务已重启：`bash scripts/manage.sh restart`
- 健康检查：`curl http://127.0.0.1:8001/api/health`

---

## 9) 0913-5：本地会议录音（自动监听）+ 纪要聚合独立化

更新时间：2025-12-26

### 目标
- 纪要聚合不再连接 Media 项目，改为系统内独立“本地自动录音 → 落盘 →（可选）转写 → 纪要聚合读取展示”。

---

## 10) 0913-6：wechat8061（8061项目）发送/消息同步/群发增强

更新时间：2025-12-27

### 发送对齐（HTTP）
- 后端发送适配 8061：支持 `POST /api/Msg/SendTxt` 载荷 `Wxid/ToWxid/Content/Type`（通过设置页配置 `wxid`）。
- 功能设置页：发送配置默认 `sendTextPath=/api/Msg/SendTxt`，并新增“发送账号(wxid)”输入框。
- 修复误填：若用户把 swagger/docs URL 粘贴到“文本接口路径”，前端会回落到正确默认值，避免配置污染。

### 消息同步（WS + 轮询）与备用数据库
- 新增后台同步任务（可开关）：轮询 `POST /api/Msg/Sync` +（可选）WS `ws://<host>:8088/ws/{wxid}` 接收推送。
- 备用数据库：`data/wechat8061_backup.db`（WAL），表 `wx_messages` 存储 `msg_id/timestamp/sender/content/raw_json`，用于应急留存。
- 新增接口：
  - `GET /api/wechat8061/sync/status`
  - `POST /api/wechat8061/sync/enable`
  - `GET /api/wechat8061/messages`

### 群发配置与个性化抬头（备注名优先）
- 群发配置持久化到后端 `ai_config.json`：`mass_send_targets/template/throttle/greeting rules`。
- 个性化称呼：`{name}` 会按“备注名>名称”生成抬头（如 `张老师/李总`），规则可配置（每行 `keyword=suffix`）。
- 新增 AI 生成群发模板接口：`POST /api/ai/mass-generate`（返回可编辑模板文本）。
- 当检测到声音时自动开始录音；声音低于阈值持续一段时间（默认 60s）自动结束本段并保存为小体积 `ogg`（或 `flac`）。

### 后端落地
- `app/services/meeting_recorder.py`：基于 `ffmpeg`(macOS avfoundation) 实现“监听 → 触发录音 → 静音超时停止 → 自动回到监听”的循环；落盘到 `data/recordings/meeting-*.ogg|flac`，并写入同名 `.json` 元数据。
- `app/routers/recorder.py`：新增 `/api/recorder/*`：
  - `GET /api/recorder/devices`：列出可用音频设备
  - `GET /api/recorder/status`：当前状态（listening/recording/silence/idle）+ 最近保存信息
  - `POST /api/recorder/start` / `POST /api/recorder/stop`：开/关自动监听
  - `GET /api/recorder/files` / `GET /api/recorder/files/{name}`：列出与下载本地录音文件
- `app/routers/minutes.py`：默认扫描 `data/minutes` + `data/recordings`；音频文件同名转写（`.txt/.md`）作为 sidecar，不重复出行；并为录音行提供 `audio_url=/api/recorder/files/<name>`。

### 前端落地
- `static/index.html`：纪要聚合筛选栏新增“会议录音”控制（设备、阈值、静音停止秒数、格式），对接 `/api/recorder/*`；保存段落时自动刷新纪要列表。

### 配置补齐
- `.env.example` 增加 `MEDIA_SERVER_BASE`（可选）与 `WHISPER_MODEL/WHISPER_LANGUAGE`（本地转写可选）。

---

## 11) 0913-7：LangBot 备用聊天记录源（适配器日志）+ AI总结分色渲染

更新时间：2025-12-29

### LangBot 项目分析结论
- `../LangBot/data/langbot.db` 内无“聊天记录”表（以知识库/流水记录为主），无法直接作为聊天记录数据源接入。
- 本项目以 LangBot 适配器导出的日志（`.jsonl/.log`）作为备用源：先写入 `adapter_messages`，再增量合并到主 `messages` 表。

### 备用源合并（去重 & 字段对齐）
- 新增合并函数：`sync_from_langbot_adapters()` 将 `AdapterMessage` 映射写入 `messages/chats/contacts`，并在 `meta` 标记 `source=langbot`。
- 去重策略：与现有 chatlog 导入一致，按 `(chat_id, sender_id, timestamp, content_text)` 跳过已存在消息。
- 新增手动接口：`POST /api/sync/langbot?days=7&force=false` 用于主动合并备用源。
- 自动合并改为可开关：功能设置 → 扩展消息，勾选“启用”后，`/api/sync/chatlog*` 同步会顺带合并备用源；未启用则不影响原流程。
- 修复扩展配置覆盖问题：`POST /api/config/extensions` 改为“合并更新”，避免保存 `ms_client_id` 时覆盖 `langbot_log_dir` 等其他扩展配置。

### 适配器日志入库去重优化
- `ingest_adapter_logs()` 在缺少 `id` 时生成稳定哈希 `external_id`（`h:<sha1>`），并在单次导入内去重，避免 `adapter_messages` 膨胀。

### AI总结渲染增强（色彩标注）
- AI 子报告：自动识别“：”前的短标签并包裹为 `k-label`，按关键词（要点/结论/风险/机会）染色，提升浏览效率。
- 新闻舆情子报告：为每条新闻条目分配不同 accent 色条与标签色，便于逐条扫描与区分。

---

## 12) 0913-8：项目备份/打包/发布（信息聚合AI系统 v0.8.0）

更新时间：2026-01-01

### 目标
- 仅保留“有用项目内容”，做一次本地备份，并发布到 GitHub（新仓库/Release）。

### 本地导出目录（干净副本）
- `/Users/lichengyin/Desktop/Projects/info-aggregation-ai-system`

### 备份包（tar.gz）
- 备份路径：`/Users/lichengyin/Desktop/Projects/0913/backups/info-aggregation-ai-system_v0.8.0_20260101-175210.tar.gz`
- 校验（sha256）：`8a5814079143ed2fdc8f3a0d1f1ccff33c0706e43fbc7b75c62eab6af4412786`
- 同目录生成 `.sha256` 文件用于核对

### 重新打包（脱敏清理后，推荐使用）
- 备份路径：`/Users/lichengyin/Desktop/Projects/0913/backups/info-aggregation-ai-system_v0.8.0_20260101-181559.tar.gz`
- 校验（sha256）：`4a71e32e62e78d8d90fb4383ff885f10b22bf2485ee79b8d2d9a8c56b24187a9`
- 说明：该包已移除旧版 `static/ui-*.png` 等可能包含真实聊天/联系人信息的截图与历史备份文件

### GitHub 发布
- 仓库：`https://github.com/leecyno1/info-aggregation-ai-system`
- Tag：`v0.8.0`
- Release：`https://github.com/leecyno1/info-aggregation-ai-system/releases/tag/v0.8.0`

### 备注（隐私与截图）
- 对外截图统一放在 `docs/assets/screenshots/`，并做了 blur 脱敏处理，避免展示真实聊天/联系人信息。
