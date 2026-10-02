# 开发与测试

## 本地要求

- Go 版本由 `go.mod` 声明，当前为 1.25。
- PDF 相关测试在发现 `xelatex`/`tectonic` 时会额外编译临时文档；CI 不需要配置 LLM 密钥。
- 实时 LLM 测试默认跳过，只有显式设置对应环境变量才会联网并产生费用。

## 测试分层

默认测试是无网络、无 API Key、无整卷生成的快速契约测试。它使用小型内存题目夹具验证编排、配额、校验、修复、配置和存储等边界；不会解析 `data/exams/`，也不会调用完整的生成/答案流水线。

需要真实题库或完整流水线时，必须显式开启对应测试：

```bash
make test-integration  # 小型样例跑完整阶段，LLM 不可用时走确定性降级
make test-corpus       # 解析并编排仓库内历年题库，可能较慢
make test-output-audit # 审计 output/live 中已持久化的试卷并尝试编译
```

实时 LLM 测试仍需另外设置 `LIVE_GENERATION_PROBE=1` 或 `LIVE_LLM=1`，默认永远跳过。

## 日常验证

```bash
make test          # go test ./...
make test-race     # go test -race ./...
make vet           # go vet ./...
make fmt-check     # 检查 gofmt
make mod-verify    # 校验 Go 模块
make build         # bin/got0genpaper
make check         # fmt-check + mod-verify + test + vet + build
make selftest      # 本机运行时、配置、模板、真题目录和写权限检查
```

CI 执行默认 race tests、`go vet` 和 CLI 构建；不会执行 `make test-integration`、`make test-corpus` 或实时 LLM 测试。可选实时测试：

```bash
LIVE_GENERATION_PROBE=1 go test ./cmd/got0genpaper -run TestLiveGenerationProbe -v
LIVE_LLM=1 LIVE_BLUEPRINT=tiny go test ./cmd/got0genpaper -run TestLiveFullPipeline -v
```

实时测试会访问 `GOT0GENPAPER_CONFIG` 指定的供应商，可能产生费用；不要在常规 CI 或 fork PR 中开启。

## 修改边界

- 新科目需同时更新 `subject_profiles.go`、题库目录、考纲摘要和对应测试。
- TOML 字段变更需同步更新配置测试、`config.example.toml` 与配置文档；禁止把本地 `config.toml` 或 API Key 放入提交。
- 流水线状态写入配置的 `data_dir`；最终文档写入 `output_dir`。测试应使用 `t.TempDir()`，不应依赖真实用户目录。
- LaTeX 样式放在 `templates/`；修改后运行 `go test ./internal/latex`，若本机有 TeX 引擎，再运行 `make pdf` 并检查 PDF。
- 批改遇到低置信度、输入重复/错位或模型失败时必须维持 `needs_review`；不可为了提高自动率静默猜分。

数据抓取与样例检查脚本的副作用、依赖和调用方式见[维护工具](TOOLS.md)。完整模块边界见[系统结构](ARCHITECTURE.md)。
