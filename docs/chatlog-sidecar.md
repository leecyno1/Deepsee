# Chatlog 本地兜底链路

Deepsee 的微信引擎采用双轨：

- **主轨道：wechatapi** — 负责实时回调、发送消息和云端部署后的在线链路。
- **兜底轨道：chatlog** — 读取本机微信聊天记录，用于历史补齐和网关异常时的本地恢复。

## 使用条件

chatlog 只能读取本机微信数据，因此必须满足：

1. 本机已登录并打开微信电脑版。
2. chatlog HTTP 服务可访问，例如 `http://127.0.0.1:5030/api/v1/session`。
3. `.env` 中配置了微信原始数据目录与 chatlog 工作目录：
   - `CHATLOG_DATA_DIR`：微信原始目录，通常是 `.../Documents/xwechat_files/<wxid>`。
   - `CHATLOG_WORK_DIR` / `CHATLOG_DIR`：chatlog 解密后的工作目录。

如果 Deepsee 部署在云服务器，云端无法直接读取用户本机微信文件。推荐架构是：

- 云端 Deepsee 使用 wechatapi 作为主链路。
- 本机 Mac/Windows 运行 chatlog sidecar。
- 本机 sidecar 定时把补齐数据同步到 Deepsee，或通过安全隧道暴露给 Deepsee 使用。

## 灰度新版 chatlog

项目提供了 sidecar 脚本，不直接替换现有 `5030` 主服务，而是使用 `5031` 做灰度验证：

```bash
bash scripts/chatlog_sidecar.sh build-v031
bash scripts/chatlog_sidecar.sh status
bash scripts/chatlog_sidecar.sh start-gray
bash scripts/chatlog_sidecar.sh logs
```

确认 `5031` 稳定后，再考虑把 `.env` 的 `CHATLOG_HTTP_BASE` 切到 `http://127.0.0.1:5031`。

首次启动可能需要二三十秒完成索引与目录扫描，脚本默认最多等待 `45` 秒。探针与日志输出会自动隐藏聊天内容和密钥。

如果需要常驻运行，使用 launchd 保活：

```bash
bash scripts/chatlog_sidecar.sh disable-old-autostart
bash scripts/chatlog_sidecar.sh launchd-install
bash scripts/chatlog_sidecar.sh launchd-status
```

`disable-old-autostart` 会停用旧的 `5030` 自动启动项，避免老版本 `--auto-decrypt` 进程反复拉起。

## 当前建议

- 不建议让 HTTP 服务长期带 `--auto-decrypt` 运行；它可能在微信数据库变化或自动解密时高 CPU 卡住。
- 更稳的方式是：HTTP 服务负责查询，解密/刷新作为单独定时任务执行。
- Deepsee 后端已对 chatlog 会话接口做快速失败：如果 `/api/v1/session` 超时，不再继续按天轮询消息，避免页面长时间卡住。

## 常见现象

- **端口开着但接口超时**：chatlog 进程假死，通常需要重启 chatlog，或拆分自动解密任务。
- **云服务器无法用 chatlog**：这是预期限制；chatlog 依赖本机微信文件。
- **消息长时间不更新**：先检查 `bash scripts/chatlog_sidecar.sh status`，再检查 Deepsee 的微信双轨状态页。
