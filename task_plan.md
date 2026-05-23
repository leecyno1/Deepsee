# Task Plan - 商业化上线前最后一公里

## Goal
以 macOS 本地单机包为首发交付形态，完成客户机器一键安装、启动、诊断、备份恢复、2G/2核轻量运行和关键链路稳定性闭环。

## Current Batch Status
- [done] 生产轻量配置：`.env.production-lite.example` 默认关闭高频后台任务，限制 AI 并发。
- [done] 就绪检查：`/api/ready` 覆盖数据库、FTS、磁盘、可写目录、模型配置、LLM Key、chatlog HTTP、后台循环、摘要队列。
- [done] 诊断报告：`/api/admin/diagnostics` 输出磁盘/数据库体积、API Key 配置状态、外部服务探测、后台任务状态、三个月聚合清理估算。
- [done] 数据保留：聚合/快照/报告/任务/评分快照仅保留最近 90 天，原始消息、邮件、联系人不自动清理。
- [done] 客户交付脚本：`scripts/manage.sh` 支持 `prod-lite`、`diagnose`、`backup`、`restore`、`launchd`、PID 自修复。
- [done] macOS 自启动：`scripts/manage.sh launchd <install|restart|status|logs|health|uninstall>` 统一封装 LaunchAgent。
- [done] UI 商业化状态组件：统一加载、空状态、失败重试、关键操作状态提示。

## Latest Verification
- `pytest -q tests/test_manage_delivery_scripts.py tests/test_commercial_readiness.py tests/test_commercial_ui_system.py tests/test_aggregation_retention.py tests/test_production_guardrails.py tests/test_sync_stability.py tests/test_messages_derive_fallback.py` → 56 passed。
- `NO_INSTALL=1 bash scripts/manage.sh restart` 后 `http://127.0.0.1:8001/api/health` 通过。
- `/api/ready` 当前为 healthy=true，核心检查全部 ok。
- `bash scripts/manage.sh diagnose` 当前报告：服务运行健康，RSS 约 170MB，数据目录约 1.2G，数据库约 1.1G。

## Next Recommended Batch
- [done] 前端真实浏览器截图验收：AI 总结、仪表盘、微信聚合、联系人、发送管理、设置页、公众号聚合。
- [done] 发送链路幂等/失败重试补强：重复目标去重，重试跳过已发送项，最后一次重试可追踪。
- [done] 低配模式连续运行压测：20 次 health 探测、ready 检查、RSS 阈值验证。
- [done] 备份恢复演练：真实备份 integrity_check 通过，临时客户目录恢复成功。
- [todo] 发布候选文档：补最终截图、已知限制、客户部署包目录结构。

## Notes
- 当前工作目录通过 `/Users/lichengyin/Desktop/Projects/0913` 访问，脚本解析到真实路径 `/Volumes/PSSD/Projects/0913`。
- `data/`、`backups/`、`.env` 为运行数据和客户配置，不应提交。

## 2026-05-04 Continued Batch
- 发送管理：创建活动时按 `target_id` 去重，去重目标写入 `campaign.meta.deduped_targets`。
- 发送重试：显式传入已发送 delivery 时不会重复发送，跳过列表写入 `campaign.meta.last_retry.skipped_already_sent`。
- 前端验收：截图报告保存到 `docs/qa-screenshots/commercial-2026-05-04/README.md`，7 个关键页面无控制台错误和横向溢出。
- 验证：关键回归 71 passed；服务已重新启动，`/api/ready` healthy=true。

## 2026-05-04 Low Resource And Backup Batch
- 新增 `scripts/customer_smoke.py`，支持低配客户机 health/ready/RSS 烟测。
- 低配烟测报告：`docs/qa-smoke/commercial-2026-05-04/low-resource-smoke.json`，RSS 约 173MB，低于 250MB。
- 备份恢复报告：`docs/qa-smoke/commercial-2026-05-04/backup-restore-drill.md`，真实备份完整性 ok，临时恢复 ok。
