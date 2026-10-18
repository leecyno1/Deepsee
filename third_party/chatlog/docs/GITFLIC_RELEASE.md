# GitFlic 自动构建与 Release

仓库根目录的 `gitflic-ci.yaml` 在 GitFlic 默认分支 `master` 更新后自动执行：

1. 启用 `sqlite_fts5` 构建标签，运行单元测试、竞态检测、`go vet` 和漏洞扫描；
2. 通过与 `make run` 相同的 `scripts/build_chatlog_binary.sh`，使用 CGO、SQLite FTS5、MMFtsTokenizer、`trimpath` 和版本注入编译 macOS `amd64`、`arm64` 两个架构；
3. 自动核验两个二进制的构建标签、CGO、目标架构和 `--version`，再生成 ZIP 和 `SHA256SUMS`；
4. 在同一 Runner 任务中创建 `build-<commit>` 预发布版本，上传两个架构的 ZIP
   和包含 `SHA256SUMS` 的校验包。

编译与发布合并在同一个任务中，避免先把约 100 MB 构建目录上传为临时流水线
Artifact，再从下一个任务下载；最终三个文件直接进入 GitFlic Release。原始二进制
保留在 ZIP 中，避免重复上传约 70 MB 内容，并降低单文件上传压力。
上传固定使用 HTTP/1.1；`SHA256SUMS` 放入独立 ZIP，兼容 GitFlic Release 对附件
媒体类型的校验。

## Runner 要求

- macOS Shell Runner；
- Runner 标签：`chatlog-macos`；
- Go 1.25 或更新版本；
- Apple Clang、`zip`、`curl`、Python 3；
- x86_64 二进制启动检查需要 Rosetta 2。

当前 GitFlic 项目已绑定标签为 `chatlog-macos` 的项目级 Runner。其他项目若在
GitFlic SaaS 界面中没有项目级 Runner 注册入口，可在 GitFlic 公司空间配置
Runner，或在 GitFlic Self-Hosted 中注册。

## 发布凭据

项目 CI/CD 变量或专用 Runner 的 macOS 登录钥匙串中配置以下变量：

- `GITFLIC_RELEASE_TOKEN`：具备当前项目 Tag 与 Release 写入权限的访问令牌。

流水线优先读取 GitFlic CI/CD 变量 `GITFLIC_RELEASE_TOKEN`。当前专用 Runner
在该变量缺失时，从登录钥匙串的 `chatlog-alpha-gitflic-release` 服务读取令牌，
账号默认使用 `CI_PROJECT_NAMESPACE`；服务名和账号可分别通过
`GITFLIC_RELEASE_KEYCHAIN_SERVICE`、`GITFLIC_RELEASE_KEYCHAIN_ACCOUNT` 覆盖。
GitFlic SaaS 的发布 API 地址通过 `GITFLIC_API_ROOT=https://api.gitflic.ru`
显式设置，避免 Web 服务地址重定向时丢失写入请求。
流水线自身的 `CI_JOB_TOKEN` 只作为发布脚本的最后回退值，独立访问令牌用于创建
Tag。令牌不写入仓库，也不会出现在流水线命令参数或日志中。

专用 macOS Runner 可用以下方式写入钥匙串（`TOKEN` 仅在当前 Shell 中提供）：

```bash
security add-generic-password \
  -U \
  -a OWNER \
  -s chatlog-alpha-gitflic-release \
  -w "$TOKEN"
```

## 本地复现

```bash
GOFLAGS='-tags=sqlite_fts5' go test ./...
GOFLAGS='-tags=sqlite_fts5' go test -race ./...
GOFLAGS='-tags=sqlite_fts5' go vet ./...
make build
scripts/build_release_macos.sh
```

`make build`、`make run`、GitFlic Release 及工作流均调用统一构建脚本；调用方已有的 `GOFLAGS` 不会改变最终二进制的功能标签。

本地发布时显式注入令牌和项目变量：

```bash
CI_PROJECT_NAMESPACE=OWNER \
CI_PROJECT_NAME=PROJECT \
CI_COMMIT_SHA="$(git rev-parse HEAD)" \
GITFLIC_RELEASE_TOKEN="$TOKEN" \
scripts/gitflic_publish_release.sh release
```
