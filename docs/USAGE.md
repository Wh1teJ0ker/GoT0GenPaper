# 使用说明

## 1. 环境准备

- Go 1.25 或更新版本：编译和运行 CLI。
- 一个兼容 OpenAI Chat Completions API 的 LLM 服务：生成题目、答案和主观题批改需要。
- `xelatex` 或 `tectonic`：编译试卷、答案和答题卡 PDF。仅运行纯文本组卷时不是必需项。
- 支持图像输入的多模态 LLM：`grade-scan` 会把当前扫描答题区域直接发送给 `vision_model`；不需要 Tesseract 或其他本地 OCR。

macOS 可用 Homebrew 安装依赖，例如：

```bash
brew install go tectonic
```

Linux/Windows 请按相应发行版安装 Go 与 TeX 引擎。项目本身不要求 Node.js、Wails 或数据库。

### LaTeX 引擎是否可以内嵌

当前默认通过 PATH 使用 `tectonic` 或 `xelatex`。可以把 Tectonic 可执行文件随各平台发布包一起分发，或用 `go:embed` 打包后启动时释放到缓存目录；但这仍需要同时处理 TeX 包、中文字体、平台二进制、代码签名和许可证，不能只嵌入一个 Go 文件就实现完全离线编译。MacTeX/TeX Live 更不适合直接内嵌，体积和依赖都较大。

因此当前版本优先保持外部 `tectonic` 的稳定路径，后续若需要一键分发，再评估“按平台附带 Tectonic + 固定包缓存”的发布方案。该方案不会改变组卷和 LaTeX 模板接口。

## 2. 安装与自检

```bash
cp config.example.toml config.toml
chmod 600 config.toml
# 编辑 config.toml 中的 base_url、模型名和 API Key
make build
./bin/got0genpaper version
./bin/got0genpaper test
```

`test` 检查 Go、TOML、active provider 参数、模板、六个科目的真题目录、输出目录写权限和 PDF 引擎。扫描批改不依赖本地 OCR；联网多模态能力使用 `test-provider --vision` 检测。

测试供应商连接会实际发出一次轻量请求：

```bash
./bin/got0genpaper test-provider
./bin/got0genpaper test-provider --provider glm-local
./bin/got0genpaper test-provider --provider glm-local --vision
```

`--vision` 会发送一张内存中生成的 PNG，确认当前模型接受图像输入。也可以在本地自检中同时检查文本和多模态连接：

```bash
./bin/got0genpaper test --network
```

## 3. 选择与测试供应商

查看已配置供应商并切换 active provider：

```bash
./bin/got0genpaper config list
./bin/got0genpaper config use glm-local
./bin/got0genpaper config show
```

添加另一个兼容供应商：

```bash
./bin/got0genpaper config add \
  --id deepseek \
  --name DeepSeek \
  --base-url https://api.deepseek.com \
  --model deepseek-chat
```

`config add` 不要求命令行传密钥。随后在权限为 `0600` 的 `config.toml` 中设置该 provider 的 `api_key`，或切换到该 provider 后通过 `NEWAPI_API_KEY` 环境变量提供密钥。不要把密钥提交到仓库或放进 shell 历史。

## 4. 生成试卷

支持 `408`、`数学一`、`数学二`、`英语一`、`英语二`、`政治`。不传 `--source` 时，默认读取配置 `data_dir` 下对应的内置历年真题目录。

```bash
# 默认 408；默认进行最多一轮整卷修复
./bin/got0genpaper generate --subject 408

# 生成数学二并编译 PDF
./bin/got0genpaper generate --subject "数学二" --compile

# 明确禁止自动修复，只生成并校验首轮结果
./bin/got0genpaper generate --subject "数学二" --max-repair-rounds 0

# 最多请求两轮整卷修复
./bin/got0genpaper generate --subject "数学二" --max-repair-rounds 2

# 指定另一份真题目录或单个输入文件
./bin/got0genpaper generate --subject 408 --source /path/to/exams
```

完整生成会对多道题分别调用模型，可能耗时较长并产生费用。请先用 `test-provider` 确认 endpoint、key 和模型权限，再开始生成。

## 5. 编译和查看产物

```bash
./bin/got0genpaper artifacts
./bin/got0genpaper compile
```

默认产物写到 `output/`：

- `paper.json`：题目、答案、题型和评分标准。
- `exam.tex`、`answers.tex`、`answer_sheet.tex`：可编辑 LaTeX 源文件。
- `exam.pdf`、`answers.pdf`、`answer_sheet.pdf`：对应 PDF。
- `spec_table.md`：题型、知识点、分值和难度规划。

如 PDF 编译失败，先检查 `./bin/got0genpaper test` 中 PDF 引擎状态，再查看 `output/*.log`。只有通过就绪检查的试卷才允许用 `compile` 编译。

## 6. 文本答案批改

答案输入是 JSON 数组，`questionId` 必须对应 `paper.json` 中的题目 ID。客观题使用 `studentOptions`（选项字母数组）或 `studentAnswer`；主观题填写完整作答文本。

```json
[
  {"questionId":"gen_spec_001","studentOptions":["B"]},
  {"questionId":"gen_spec_017","studentAnswer":"写在这里的解题过程"}
]
```

运行批改：

```bash
./bin/got0genpaper grade \
  --paper output/paper.json \
  --answers answers.json \
  --output output/grades.json
```

缺交题会记为 `blank`；重复题号、未知题号、识别不确定和无法判定的主观答案会进入 `needs_review`。主观题在 LLM 不可用时不会自动给满分。输出含逐题评分、总分、满分和复核列表。

## 7. 扫描答题卡批改

扫描输入属于后续实验范围，不参与当前试卷生成主链的稳定性判断。当前生成链只要求输出 `paper.json`、LaTeX 源文件和 PDF；PDF/PNG 扫描格式适配会在输入协议确定后单独演进。

使用本工具生成的答题卡，扫描时保留页面四角的机读定位块。图片支持 PNG/JPEG；传入整套答题卡对应页：

```bash
./bin/got0genpaper scan \
  --paper output/paper.json \
  --images scans/page-01.jpg scans/page-02.jpg \
  --segments-dir output/segments \
  --output output/scan.json

./bin/got0genpaper grade-scan \
  --paper output/paper.json \
  --images scans/page-01.jpg scans/page-02.jpg \
  --segments-dir output/segments \
  --output output/grades-scan.json
```

也可以重复使用 `--images`：`--images page-01.jpg --images page-02.jpg`。程序会从每张扫描图检测定位标记并估计变换，再根据扫描页面的结构分割答题区域，不要求与原 PDF 像素坐标完全一致。`grade-scan` 将每个分割图片和题目/评分标准发送给配置中的 `vision_model`，客观题和主观题都走多模态模型；模型响应会再次校验。定位失败、涂写模糊、模型不可用或输出格式异常都会标记 `needs_review`，不能跳过人工复核。

状态含义：`recognized` 为达到阈值的识别，`blank` 为确定空白，`ambiguous` 为多涂/不确定，`needs_review` 为不能安全自动处理。`--allow-uncalibrated` 仅供诊断，不能用于正式自动评分。

## 8. 路径与配置

```bash
# 使用另一个 TOML 文件
GOT0GENPAPER_CONFIG=/secure/path/paper.toml ./bin/got0genpaper test

# 临时覆盖当前 active provider 的 key
NEWAPI_API_KEY="$PAPER_LLM_KEY" ./bin/got0genpaper test-provider
```

支持覆盖模型、Base URL 和分题型模型的完整变量清单见[配置说明](CONFIGURATION.md)。相对 `data_dir`、`output_dir` 和 `templates` 路径以 TOML 文件所在目录为基准。
