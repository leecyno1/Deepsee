# Chatlog Alpha

> [!IMPORTANT]
> ## 用户订阅（置顶）
> 因收到腾讯方面关于侵权问题的投诉，GitHub 仓库已停止公开源代码、构建产物和历史 Release，仅保留项目说明。
>
> **项目状态、更新通知与用户订阅入口：**
> **[加入 Telegram 订阅](https://t.me/+zy1N_8leq8IxYmY8)**

Chatlog Alpha 是面向微信 4.x 的本地聊天数据读取与自动化工具。项目用于发现本机微信账号、提取和管理密钥、直接只读查询加密 WCDB、解密媒体，并通过 TUI、Web、结构化 CLI 和本机 HTTP JSON API 提供统一入口。

## 当前项目状态

- GitHub 云端仓库仅保留本 README，不再公开项目源代码或二进制构建产物。
- GitHub 推送入口已关闭；当前代码同步、macOS 自动验证、双架构构建与预发布 Release 流程迁移至 GitFlic，详见 [`docs/GITFLIC_RELEASE.md`](docs/GITFLIC_RELEASE.md)。
- 当前项目已移除原生文本/图片发信、发信调试、常驻注入 Session、语义检索、时序图谱和 MCP 实验功能。
- 当前项目仅支持 macOS；其他桌面系统的实现、构建任务和发布产物已完整移除。
- 应用已调整为 composition root + 用例 + 端口 + 可替换适配器的模块化架构；当前验收评分 **10/10**，详见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) 和 [`docs/MODULARITY_SCORECARD.md`](docs/MODULARITY_SCORECARD.md)。
- 当前保留的核心方向是本地数据库直读、媒体处理、结构化查询、消息推送和本机自动化接口。
- 后续状态变化、版本说明和用户通知统一通过置顶的 Telegram 订阅入口发布。

### 更新日志（当前项目状态节点：2026-08-03）

- **日志管理**：运行日志按本地日期每日零点轮转，单文件达到 500 MB 时提前轮转；历史日志自动压缩，默认保留 7 天，可在 1–365 天范围内配置。写入、轮转、配置更新和关闭统一串行化，避免运行期调整保留天数产生竞态。
- **仪表盘与可观测性**：仪表盘、数据库、聊天数据分析已拆分；逐文件追踪支持写入阈值自动暂停，并按单次追踪周期保留读/写 Top 100 文件排名；全库解析审计调整为手动执行，避免周期性生成大型查询缓存。
- **运行控制与排查**：仪表盘提供分类日志、HTTP 错误记录、慢查询、内存与 GC、缓存文件路径、快捷打开、分类清理，以及结束 Chatlog、清理关联进程和注入并重启微信的操作；缺失 favicon 引起的 HTTP 404 已消除。
- **数据库与搜索**：全部运行时查询已切换为加密数据库只读 VFS，按 SQLite 请求在内存中解密页面，并以内存覆盖层合并 WAL 最后提交态；不再生成完整解密库或更新 `wcdb` 查询缓存。业务模块与辅助数据支持详情、分页和检索；解析覆盖率、分片/FTS 水位及资源关联可审计；最新消息使用数据库变更流增量读取，不执行周期兜底扫描。
- **消息与媒体解析**：统一聊天卡片预览已覆盖文本、图片、视频、语音、附件、位置、红包、系统消息和朋友圈内容；补齐 `BrandSessionHolder`、红包领取文本、位置卡片、朋友圈多图/文章/互动及媒体关联解析，并修正历史消息发送者归属和图片路由 404 问题。
- **OCR 与图片 Hook**：支持实时/历史、监听范围、本地/API 模式和完整结果；缩略图不进入识别，监听范围内的新图片仅在微信原生收图请求中升级中图，旧缩略图重试会刷新原生请求预授权。生产路径已停用合成私有函数调用，避免媒体索引缺失、复制图片损坏和客户端异常；附件读取新增内容签名校验，可将扩展名为图片但内容仍加密的文件关联到已落盘原图。Apple MLX 推理后端与 GLM-OCR SDK 作为一个进程组同步启动、停止，任一端启动失败时整组回滚。
- **推送与队列**：实时全部转发及规则匹配均从本次生效时间之后开始；关键词支持文字、图片 OCR 和文字 + 图片 OCR 混合模式，图片始终只在 OCR 命中后推送；持久投递记录支持单条及批量重试、删除。
- **构建发布**：`make run` 通过增量依赖检查后运行 `bin/chatlog`，源码与内置资源未变化时不会重复写入二进制；普通 `go run .` 检测到缺少完整构建标签时，会自动增量构建并切换到同一 `bin/chatlog`，避免 FTS5 初始化失败连带跳过 OCR 与 Frida 图片 Hook。本地构建、`make run`、双架构 Release 和工作流统一调用同一构建脚本，固定启用 CGO、SQLite FTS5、MMFtsTokenizer、`trimpath` 与版本注入，并在产物生成后自动核验构建元数据和 `--version`。数据库密钥/图片下载 Frida、Hermes 桥接及本地 OCR 安装、配置、启停资源均已内置，并按内容版本一次性落盘。GitFlic `master` 更新后由 macOS Runner 自动执行双架构预发布流程。

## 当前能力

- 多进程、多账号发现与切换
- macOS 数据库密钥、图片密钥提取
- 加密 WCDB 页面按需直读、提交态 WAL 内存覆盖
- 会话、消息、联系人、群聊、收藏和朋友圈查询
- 图片、视频、语音与附件读取、解密
- 数据库浏览、跨库搜索、只读 SQL、CSV/Excel 导出
- 实时运行仪表盘、项目及衍生进程文件 I/O、分类日志、HTTP 错误和缓存管理
- 数据库解析覆盖率、分片/FTS 水位、一致性审计和消息资源关联
- GLM-OCR 识别、中文文搜图、监听范围图片自动下载与预览（vLLM 默认，MaaS 可切换）
- POST、Hermes Weixin、Hermes QQ 消息推送
- 面向 LLM 的自描述 CLI 与机器可读 HTTP API 目录
- TUI、Web 控制台与结构化 `chatlog ops` / `chatlog api` 接口

## 已移除功能

以下能力已从当前项目代码中完整移除：

- `chatlog send` 原生文本/图片发送 CLI
- `/api/v1/send-debug/*` 原生发信调试接口
- Web 控制台“发信调试”页面及其静态资源
- Frida 原生发信脚本、图片上传 Agent、欢迎消息与常驻 Session supervisor
- 语义检索、Embedding、MCP 和时序图谱实验模块

聊天图片读取、图片密钥提取、媒体解密和本地预览仍属于当前项目的数据读取能力，不属于原生发图功能。

## 安全边界

Chatlog 可读取本机完整聊天数据，应仅在可信本机环境运行：

- HTTP 仅监听 `localhost`、`127.0.0.0/8` 或 `::1`，默认地址为 `127.0.0.1:5030`。
- 数据库以只读和 `query_only` 模式连接。
- 配置、密钥及 Hermes 环境文件使用受限文件权限。
- Hermes Token 与 Secret 只写入配置，不通过状态接口回显。
- 朋友圈媒体代理限制私网、loopback、link-local、用户信息 URL、协议及响应大小。
- Web 控制台不应通过反向代理、端口映射或隧道公开。

## GLM-OCR 文搜图

项目提供完整的图片 OCR 链路：默认由 vLLM 部署 `zai-org/GLM-OCR`，
官方 SDK Server 负责版面检测和区域并行识别；也可切换到智谱 MaaS。
识别结果写入独立 SQLite sidecar，并使用 FTS5 trigram 中文子串索引，
不会修改微信原始数据库。独立「OCR 识别」页面提供实时/历史开关、本地/API
模式、指定联系人与群聊范围、详细队列状态和文搜图；消息预览会在图片下方显示
简洁描述。

部署、配置、增量水位、历史补录和 API 说明见
[`docs/GLM_OCR.md`](docs/GLM_OCR.md)。
Apple Silicon Mac 可使用官方 MLX 路径安装并健康检查本地服务，也可选择让模型服务跟随项目自动启动；
首次加载模型期间，OCR 页面会显示具体阶段。

## 当前接口概览

当前 HTTP Catalog 包含 72 个 endpoint，覆盖：

| 分类 | 数量 | 说明 |
| --- | ---: | --- |
| system | 15 | 健康检查、运行状态、进程结束、分类日志、逐文件 I/O、缓存、API Catalog、OpenAPI |
| chat | 8 | 会话、历史、搜索、未读、群成员、增量消息、统计与关键词分析 |
| social | 4 | 收藏、朋友圈通知、动态与搜索 |
| address_book | 3 | 联系人、群聊、OpenIM 企业信息 |
| database | 11 | 数据库模块、审计、跨库搜索、表、数据、只读 SQL、辅助语义数据 |
| media | 6 | 图片、视频、语音、附件、数据文件、朋友圈媒体代理 |
| push | 11 | 推送配置、状态、事件、单条/批量持久投递控制、Hermes Weixin/QQ |
| ocr | 14 | OCR 配置、实时状态、文搜图、历史补录、中图下载重试、重新识别、清空和本地模型控制 |

账号选择、密钥提取、服务生命周期和持久配置仍由本机 TUI 或结构化 `chatlog ops` 管理。

## 运行数据

默认持久化根目录为 `~/.chatlog`：

```text
~/.chatlog/
├── chatlog.json
├── chatlog.pid
├── log/
│   ├── chatlog.log
│   └── chatlog-*.log.gz
├── cache/
└── workspaces/<account>/
    ├── runtime/hook.db
    ├── ocr/image_ocr.db
    └── cache/
```

`chatlog.log` 每日轮转，单文件上限为 500 MB；轮转文件使用 gzip 压缩并默认保留 7 天。可通过 `chatlog ops config-set --log-retention-days 30` 调整，支持范围为 1–365 天，配置写入当前账号并在后续启动或账号切换时自动应用。

`cache/wcdb` 仅用于展示和清理旧版本遗留文件；当前数据库查询不会在该目录创建、改写或读取完整解密库。

微信原始数据目录由微信自身管理，不属于 Chatlog 的运行根目录。

## 获取通知

GitHub 当前不提供源代码、Release 或安装包下载。项目状态及后续通知请使用置顶入口：

### [Telegram 用户订阅：https://t.me/+zy1N_8leq8IxYmY8](https://t.me/+zy1N_8leq8IxYmY8)

## 贡献致谢

感谢以下贡献者曾提交问题复现、实测数据与实现方案。相关功能后续即使重构或下线，贡献记录仍予保留：

| 贡献者 | PR / MR | 贡献摘要 |
| --- | --- | --- |
| [@ameriadam12](https://github.com/ameriadam12) | [#90](https://github.com/teest114514/chatlog_alpha/pull/90) | 大型数据集下仪表盘与历史查询性能优化。 |
| [@onewilk](https://github.com/onewilk) | [#81](https://github.com/teest114514/chatlog_alpha/pull/81)–[#85](https://github.com/teest114514/chatlog_alpha/pull/85)、[GitFlic !1](https://gitflic.ru/project/goring/chatlog_alpha/merge-request/1) | OpenIM、Webhook、群聊回填、缓存刷新、群聊 @ 解析，以及日志轮转与保留策略方案。 |
| [@TnzGit](https://github.com/TnzGit) | [#68](https://github.com/teest114514/chatlog_alpha/pull/68) | 历史 Embedding 响应截断修复；相关实验功能现已下线。 |
| [@ouyadi](https://github.com/ouyadi) | [#64](https://github.com/teest114514/chatlog_alpha/pull/64)、[#65](https://github.com/teest114514/chatlog_alpha/pull/65) | MCP、语义检索、索引与消息钩子安全改进；仍适用的修复继续保留。 |
| [@marswjf](https://github.com/marswjf) | [#59](https://github.com/teest114514/chatlog_alpha/pull/59)、[#60](https://github.com/teest114514/chatlog_alpha/pull/60) | Action CLI、媒体与历史账号支持。 |
| [@think2011](https://github.com/think2011) | [#54](https://github.com/teest114514/chatlog_alpha/pull/54) | Webhook、WAL、文件锁与时间范围问题验证。 |
| [@jingmian](https://github.com/jingmian) | [#18](https://github.com/teest114514/chatlog_alpha/pull/18) | 图片密钥扫描实测建议。 |
| [Dependabot](https://github.com/apps/dependabot) | [#23](https://github.com/teest114514/chatlog_alpha/pull/23)、[#75](https://github.com/teest114514/chatlog_alpha/pull/75)、[#80](https://github.com/teest114514/chatlog_alpha/pull/80) | 依赖更新与安全修复建议。 |

## 声明

Chatlog Alpha 为非官方项目，与腾讯或微信不存在隶属、授权或合作关系。“腾讯”“微信”及相关名称和标识归其权利人所有。使用者应遵守所在地法律法规、软件许可和平台规则，并自行负责本地数据与账号安全。
