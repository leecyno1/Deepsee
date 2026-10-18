# 模块化评分表

评估日期：2026-07-29  
当前评分：**10 / 10**

评分采用十项二值验收，每项 1 分。只有实现证据和自动验证同时存在才计分。

| # | 验收项 | 分值 | 实现证据 | 自动证据 |
| ---: | --- | ---: | --- | --- |
| 1 | 唯一 composition root | 1 | `internal/chatlog/modules` 建立完整共享服务图 | `TestCompositionBoundaries` |
| 2 | 依赖方向受控 | 1 | `ports`、`usecase`、HTTP、Database 分层 | `TestLayerDependencyDirection` |
| 3 | 具体适配器隔离 | 1 | Darwin、WCDB、Media 只在 `modules/adapters` 绑定 | `TestConcreteAdaptersStayBehindCompositionRoot` |
| 4 | 应用流程用例化 | 1 | Keys、Decryption、Sessions、Server 四个用例 | usecase 单元测试与全量测试 |
| 5 | 跨层契约类型化 | 1 | query/hook/key/media/database state 均使用 `ports` | 编译期 interface checks 与架构测试 |
| 6 | HTTP 按 feature 模块化 | 1 | chat/social/contacts/media/database/hooks 独立注册 | HTTP catalog、OpenAPI、route tests |
| 7 | 热点文件完成职责拆分 | 1 | WeChat、MessageHook、WCDB、TUI 均已拆分 | `TestProductionGoFilesStayFocused` |
| 8 | 配置按 capability 拆分 | 1 | HTTP、Database、WeChat 使用 focused config interfaces | 编译期接口满足与模块测试 |
| 9 | 生命周期集中且可回滚 | 1 | `modules.Services` + `usecase.Server` | lifecycle、composition tests 与 race suite |
| 10 | 架构可持续验证 | 1 | 架构文档、评分表、typed context contract | test、race、vet、build、diff check |

## 10 分门槛

以下任一回归会直接失分并使 CI/本地验收失败：

- 在 composition root 外创建 WeChat、Database、HTTP 或 MessageHook 服务；
- `ports` 反向依赖 transport、storage implementation 或 adapter；
- HTTP 直接导入 Database/MessageHook 实现、WCDB 或 `dat2img`；
- 应用层直接导入 Darwin、WCDB datasource 或具体 media adapter；
- 生产 Go 文件超过 800 行；
- 在 typed request helper 之外使用 `context.WithValue`。

## 验收命令

```bash
go test -tags sqlite_fts5 ./...
go test -tags sqlite_fts5 -race ./...
go vet -tags sqlite_fts5 ./...
make build
go test -tags sqlite_fts5 ./architecture
git diff --check
```

评分不是静态自评；`architecture/contracts_test.go` 把关键边界转为可执行契约，完整工具链验证负责行为、并发、静态分析和构建闭环。
