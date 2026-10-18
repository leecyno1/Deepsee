<p align="center">
  <img src="static/assets/deepsee-repo-header.png" alt="Deepseek 深瞳：Agent 可接入的全领域个人信息流 API 平台" width="920">
</p>

<p align="center">
  <img src="docs/assets/deepsee-logo.png" alt="Deepsee 深瞳 Logo" width="180">
</p>

<h1 align="center">Deepsee · 深瞳</h1>
<p align="center"><strong>你的信息流，你的记忆，你的 Agent。</strong><br>Local-first intelligence workspace · Visual platform + Data API</p>

<p align="center">
  面向 Agent 接入的个人信息流底座，把微信、邮件、会议、新闻、自媒体、公众号与联系人观点统一沉淀为可查询、可推理、可验证的 API 数据层。
</p>

<p align="center">
  <a href="#核心定位">核心定位</a> ·
  <a href="#功能模块">功能模块</a> ·
  <a href="#agent-api">Agent API</a> ·
  <a href="#安装">安装</a>
</p>

<p align="center">
  <img alt="Python" src="https://img.shields.io/badge/Python-3.11+-3776AB?style=flat-square&logo=python&logoColor=white">
  <img alt="FastAPI" src="https://img.shields.io/badge/FastAPI-Agent%20API-009688?style=flat-square&logo=fastapi&logoColor=white">
  <img alt="SQLite" src="https://img.shields.io/badge/SQLite-Local%20First-003B57?style=flat-square&logo=sqlite&logoColor=white">
  <img alt="Agent Ready" src="https://img.shields.io/badge/Agent-Ready-0A84FF?style=flat-square">
</p>

## 核心定位

Deepsee / 深瞳不是单一消息工具，而是一套面向个人与团队 Agent 的信息流操作系统：

- **统一接入**：汇聚微信、邮件、会议纪要、新闻、自媒体、公众号等高频信息源。
- **结构化沉淀**：将分散内容转为消息、联系人、主题、观点、资产、摘要、评分等可复用数据。
- **Agent 友好**：通过 API 暴露搜索、摘要、评分、趋势、联系人画像与一页通生成能力。
- **推理闭环**：支持 AI 分析、情感洞察、趋势预测、观点验证与联系人价值评分。
- **本地优先**：默认 SQLite 本地存储，适合个人私有部署，也可迁移到云服务器。

## 功能模块

| 模块 | 说明 |
|------|------|
| 数据看板 | 聚合趋势、关键词、热度、情绪、机会/风险信号与运行状态 |
| 微信引擎 | 微信消息接入、清洗去重、黑白名单、摘要与触发规则 |
| 邮件引擎 | 多账户同步、邮件摘要、问答/顶踩与回复链路 |
| 会议引擎 | 会议原文接入、纪要抽取、主题归档与分析注入 |
| 新闻引擎 | 内置新闻采集、热度评分、中文化摘要与趋势排序 |
| 自媒体引擎 | 小红书、抖音、微博、B站等轻量采集与关键词热点搜索 |
| 公众号引擎 | 公众号文章检索、去重、摘要与主题沉淀 |
| 联系人评分 | 基于观点命中、服务价值、风险提示、交流密度的联系人评分卡 |
| 消息群发 | 活动编辑、敬语规则、发送管理与多对象群发 |
| 微信自动化 | 独立开关、触发范围、人类回复抑制、限流与阶段性聊天记忆 |
| 模型及 Agent 配置 | 检测 Codex / Hermes / Qoder / Claude；探测子模型；高级处理、轻量处理两档路由 |
| AI 分析 | 一页通、统一提示词、摘要缓存与增量生成 |

### 一套任务逻辑，两种 AI 执行方式

内置模型 API 与外接 Agent 可以混用。例如高级处理（含一页通）交给 Hermes，轻量处理使用直连模型；也可以两档都用 Agent、分别选择子模型。Qoder 读取 CLI 模型列表，Hermes 读取所配服务的 `/models`，Codex 读取本地配置/缓存，Claude 读取 CLI 模型别名。列表标明来源，不代表账号实时调用权限；均保留自定义模型 ID 和连通测试入口。

- **提示词不搬家**：任务提示词与聊天记忆由 Deepsee 组装，经统一入口交给模型或 Agent。
- **记忆有边界**：最近 30 / 50 / 100 条，另设时间和 token 上限；默认群聊按发送人隔离，重置不删除原始记录。
- **Agent 专注处理**：CLI 使用隔离临时目录，禁用工具和持久会话；不会直接操作微信。自动发送仍由 Deepsee 的回复规则控制。
- **采集可扩展**：RSS / Atom、V2EX 可直接读取；更多社交平台可通过 Agent Reach 检测并使用 OpenCLI。平台登录、浏览器扩展和限流仍会影响可用性。

安装检测不等于已登录或调用成功。选择 Agent 后，请保存并运行对应任务的连通测试。

![模型及 Agent 配置界面](docs/assets/model-agent-two-tiers.jpg)

## Agent API

Deepsee 适合作为 Agent 的个人信息流工具层。Agent 可以围绕这些能力构建自动化工作流：

| 能力 | 示例用途 |
|------|----------|
| 消息检索 | 查找某联系人、主题、关键词、时间段内的历史信息 |
| 摘要提炼 | 对微信、邮件、新闻、公众号、会议原文生成要点与一句话评论 |
| 趋势洞察 | 识别高频主题、情绪变化、热点上升、风险信号 |
| 联系人画像 | 读取联系人评分卡、观点历史、命中验证和服务价值 |
| 一页通生成 | 将多模块内容重组为结构化报告或图文材料 |
| 自动触发 | 基于网关规则、黑白名单、时间任务和模型路由执行动作 |

常用接口示例：

| 端点 | 用途 |
|------|------|
| `GET /api/v1/messages` | 消息列表、搜索与增量读取 |
| `GET /api/v1/contacts` | 联系人备注与标签 |
| `GET /api/v1/contacts/{contact_id}/scorecard` | 联系人评分与已保存证据 |
| `GET /api/v1/articles?source=media` | 自媒体本地快照；也支持 news / mp |
| `GET /api/v1/meetings` | 会议文字及已有转写 |
| `GET /api/v1/search` | 跨来源字面搜索 |
| `GET /api/v1/capabilities` | 当前项目可用权限 |
| `GET /api/v1/openapi.json` | 独立 API 契约 |

新项目使用只读 `/api/v1`，在“功能设置 → 开发者中心”创建独立项目密钥。**内网也需要 Bearer 密钥**，避免将聊天数据匿名开放。API 读取已有数据，不会因查询而自动抓取、调用模型或发消息。

```bash
curl --fail-with-body --get \
  -H "Authorization: Bearer $DEEPSEE_API_KEY" \
  --data-urlencode 'q=半导体' \
  'http://127.0.0.1:8001/api/v1/messages'
```

完整权限、分页、附件和接入示例见 [项目数据 API](docs/project-data-api.md)。旧 `/api/agent` 保持兼容，但不建议新项目依赖页面管理接口。

## 安装

```bash
git clone https://github.com/leecyno1/Deepsee.git
cd Deepsee
cp .env.example .env
bash scripts/manage.sh install
bash scripts/manage.sh start
```

本地访问：

```bash
uvicorn app.main:app --host 127.0.0.1 --port 8001
```

打开浏览器访问 `http://127.0.0.1:8001/`。

### 两种运行方式

| 方式 | 前端入口 | 数据与配置 |
| --- | --- | --- |
| 独立项目 | 浏览器打开 `/`，包含完整导航和设置 | 使用此实例的本地数据库与配置 |
| Newma Desk 模组 | Desk 中打开深瞳，使用 `/embed/{module}` 页面 | 使用 Desk 配置的运行副本；与该副本的独立浏览页共用后端 |

例如 `/embed/message-list`、`/embed/wechat-automation`、`/embed/function-settings`。两种前端共用功能代码，不需要维护两套模型配置。若独立启动了另一份服务，则不会自动共享数据；请明确服务地址和运行目录，避免同时占用 8001 端口。

### 数据与开源边界

数据库、聊天记录、媒体、录音、采集结果与 API Key 不应提交到仓库。对外部署前配置鉴权、HTTPS 与访问限制，禁止公开开发者管理接口。历史版本中出现过敏感文件；未完成历史清理前，应将旧密钥视为已暴露并轮换。不要将已有 Git 历史当作经过脱敏的发布包。

### 内置 Chatlog 依赖

用于本机微信历史补齐的 Chatlog 源码已收录在 `third_party/chatlog/`，无需再从其他
Chatlog 仓库下载。首次使用时在本机编译：

```bash
bash scripts/build_chatlog.sh
```

也可以让本地依赖安装器完成构建：

```bash
python scripts/install_wechat_local_deps.py --tool chatlog_alpha
```

需要 Go 1.24+ 和 CGO 编译环境；生成文件位于 `.local/`，不会提交到 Git。
Chatlog 的 MIT 许可证和免责声明随源码保留在 `third_party/chatlog/`。

## WeChat API 对接

Deepsee 可通过 wechatapi.net 的 iPad 协议接入微信，也可在云服务器侧与 Hermes 等服务组合部署。配置要求：

1. wechatapi token + app_id（从 [wechatapi 控制台](https://wechatapi.net/) 获取）
2. 回调公网 URL（云服务器直接 IP 或域名，无需隧道）
3. AI 模型路由配置（用于自动摘要、分析与回复）
4. Agent 端配置（见下方 wx-auto 配套包）

部署与排障补充：

- 参见 [`docs/wechat-gateway-deployment-notes.md`](docs/wechat-gateway-deployment-notes.md) 获取公网回调绑定、登录状态持久化、WeChatAPI 人脸验证和子 session 行为说明。
- 直连公网 IP 部署时，Deepsee 需要监听 `0.0.0.0`，否则 wechatapi.net 访问公网 callback 可能报 `push msg err`。
- 登录成功后保存 token/app_id/wxid/region_id/device_type 的对应关系，二次登录优先复用 app_id。

触发规则（2026-05 更新）：

- `at_mention_enabled` 默认开启，群聊中被 @ 时自动触发回复。
- 自动回复范围已放宽：除违法内容和系统配置指令外，日常闲聊也可正常互动。
- 黑白名单、前缀匹配、正则匹配、随机触发等规则可通过 `POST /api/wechat-gateway/trigger-rules` 配置。

### Agent 配套包

**https://github.com/leecyno1/wx-auto** 提供 Hermes/Agent 侧的：
- 完整 WeChatAPI 协议文档（7 个模块，100+ 端点）
- 云服务器一键部署脚本
- Hermes Skill 模板
- 新环境安装指引

文档边界：
- 本仓库（Deepsee/0913）内部署/迁移的唯一维护文档：`DEPLOY_FULL.md`
- `wx-auto/DEPLOY.md` 是独立的 Agent 侧 / 跨机部署手册，不替代本仓库内部部署文档

## 开发

```bash
python -m pip install -r requirements-dev.txt
python -m pytest -q
bash scripts/manage.sh dev
```

## 许可证

Apache-2.0 — see `LICENSE`.
