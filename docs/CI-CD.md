# CI/CD 与版本发布

本项目的 CI/CD 面向命令行程序：普通分支/PR 做测试、静态检查和构建；推送 `vX.Y.Z` 标签后制作多平台分发包并创建 GitHub Release。工作流位于 `.github/workflows/`。

## 版本来源

- `VERSION`：本地构建版本的单一来源，当前为 `0.1.1`。
- `make build`：读取 `VERSION` 并通过 Go linker flag 注入 `main.version`。
- Git 标签：正式发布版本，例如 `v0.1.1`；发布工作流会使用 `GITHUB_REF_NAME` 覆盖本地版本。
- `got0genpaper version`：查看最终编译进二进制的版本。

发布前确认目标提交已包含源码、测试和文档，并通过 CI；然后创建标签并推送：

```bash
git tag -a vX.Y.Z -m "GoT0GenPaper vX.Y.Z"
git push origin main
git push origin vX.Y.Z
```

## CI 检查

`.github/workflows/ci.yml` 在 push、pull request 和手动触发时运行：

1. 从 `go.mod` 读取 Go 版本并下载依赖。
2. 检查所有 Go 源文件已通过 `gofmt`。
3. 执行 `go mod verify` 验证依赖完整性。
4. 执行 `go test -race ./...`。默认测试使用内存夹具，不解析完整历年题库，也不生成整套试卷。
5. 执行 `go vet ./...`。
6. 构建 CLI。

CI 不访问 LLM，不需要 `config.toml`、API Key、TeX 或 OCR。实时文本/多模态测试必须由开发者在本机手动开启，不能把真实密钥放进普通 PR 工作流。

需要在本机验证完整阶段或真实题库时，显式执行：

```bash
make test-integration  # 完整阶段，但使用小型样例和离线降级
make test-corpus       # 真实 bundled corpus，可能较慢
make test-output-audit # 仅在存在 output/live/paper.json 时运行
```

## 发布工作流

`.github/workflows/release.yml` 仅响应 `v*` 标签：先测试，再构建以下无 CGO 分发包，最后创建 GitHub Release 并附 SHA-256 校验文件：

- Linux：amd64、arm64
- macOS：amd64、arm64（Apple Silicon）
- Windows：amd64

每个平台的分发包包含对应 CLI、`README.md`、`LICENSE`、`config.example.toml` 和 `docs/`：

- Linux/macOS：`.tar.gz`
- Windows：`.zip`
- 所有分发包：`SHA256SUMS`

发布前更新变更记录并提交，然后在 GitHub 仓库推送版本标签：

```bash
git tag -a vX.Y.Z -m "GoT0GenPaper vX.Y.Z"
git push origin main
git push origin vX.Y.Z
```

在 GitHub 的 Actions 页面等待 `Release` 工作流成功。Release job 使用仓库内置 `GITHUB_TOKEN`，无需额外 PAT；仓库设置须允许 Actions 创建 releases。不要从未通过检查的 commit 打标签。

## 仓库状态

项目已连接公开 GitHub 仓库 [`Wh1teJ0ker/GoT0GenPaper`](https://github.com/Wh1teJ0ker/GoT0GenPaper)，默认分支为 `main`，远程名为 `origin`；CI 和标签发布工作流均已配置。正常使用不需要重新创建仓库或配置远程。

若发布工作流未能创建 Release，检查 GitHub 仓库 Actions 是否启用，以及工作流是否有 `contents: write` 权限。推送前仍需审阅暂存清单；若 API Key 曾进入 Git 历史，应立即在供应商控制台撤销或轮换，单纯删除文件不能使其失效。

## 本地模拟 CI

```bash
make check
make fmt-check
make mod-verify
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/got0genpaper-linux-amd64 ./cmd/got0genpaper
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o /tmp/got0genpaper-darwin-arm64 ./cmd/got0genpaper
```

发布包只包含 CLI、README、许可证、配置模板和文档，不捆绑真题、密钥或 TeX 引擎。使用者需单独准备题库、配置和 PDF 编译引擎；扫描批改直接使用供应商的多模态模型，不需要本地 OCR。
