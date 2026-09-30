# GoT0GenPaper

[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![CLI](https://img.shields.io/badge/interface-CLI-2F80ED?style=flat&logo=gnubash&logoColor=white)](#先跑起来)
[![TOML](https://img.shields.io/badge/config-TOML-9C4121?style=flat)](https://toml.io/)
[![LaTeX](https://img.shields.io/badge/output-LaTeX-008080?style=flat)](https://www.latex-project.org/)
[![Vision LLM](<https://img.shields.io/badge/grading-Vision%20LLM-7C3AED?style=flat>)](#扫描答题卡批改)
[![License: MIT](https://img.shields.io/badge/license-MIT-2ea44f?style=flat)](LICENSE)
[![Version](https://img.shields.io/badge/version-0.1.0-3b82f6?style=flat)](VERSION)

基于预测知识点，调用大模型生成一套可以打印的考研模拟试卷。

历年真题和考纲在这里主要用于分析命题规律、提取知识点和约束组卷方向；真正的试题由模型重新生成，不是简单拼接历年真题。

GoT0GenPaper 是一个纯命令行工具，当前已配置以下科目入口：

- 408
- 数学一
- 数学二
- 英语一
- 英语二
- 政治

它可以完成组卷、答案生成、评分标准生成、答题卡生成和 PDF 编译。当前只有数学二完成过一轮完整的生成、校验和 PDF 流程测试；其他科目虽然已经保留入口和数据结构，但还不能视为同等稳定。

> **批改功能提示：** 扫描答题卡、视觉模型识别和自动批改功能尚在研究中，当前不作为稳定能力承诺，敬请期待。

> **Tips:** 这是帮我可怜的考研朋友们做的一个小玩具。它适合用来练习和获得额外题目，不是官方真题，也不承诺押中考试；请把模型生成内容当作模拟材料，并自行核对答案和知识点。

## 先跑起来

### 1. 准备环境

需要安装：

- Go 1.25 或更新版本
- 一个兼容 OpenAI Chat Completions 接口的模型服务
- `tectonic` 或 `xelatex`，用于生成 PDF

macOS 可以直接安装基础依赖：

```bash
brew install go tectonic
```

如果暂时只想生成 JSON 和 LaTeX 源文件，PDF 引擎可以稍后再安装。

### 2. 创建本地配置

复制配置模板：

```bash
cp config.example.toml config.toml
chmod 600 config.toml
```

然后编辑 `config.toml`，至少确认以下配置：

```toml
[paths]
data_dir = "./data"
output_dir = "./output"
templates = "./templates"

[providers]
active_id = "glm-local"

[[providers.providers]]
id = "glm-local"
name = "Local GLM 5.2"
base_url = "http://127.0.0.1:3001"
api_key = ""
default_model = "glm-5.2"
judge_model = "glm-5.2"
vision_model = "glm-5.2"
enabled = true
```

远程供应商只需要把 `base_url`、`api_key` 和模型名换成自己的值。`config.toml` 含有密钥，只保存在本机，不要提交到仓库。

### 3. 编译并自检

```bash
make build
./bin/got0genpaper version
./bin/got0genpaper test
```

`test` 会检查 Go 运行时、TOML 配置、题库目录、模板、输出目录权限和 PDF 引擎。

如果还要测试真实的模型连接：

```bash
./bin/got0genpaper test-provider
./bin/got0genpaper test-provider --vision
./bin/got0genpaper test --network
```

其中 `--vision` 会发送一张最小 PNG 图片，确认当前模型是否真正支持图像输入。测试会产生请求，可能消耗少量额度。

### 4. 生成一套试卷

例如生成数学二：

```bash
./bin/got0genpaper generate --subject "数学二" --compile
```

这条命令会完成题目生成、答案生成、评分标准生成、质量校验，并在最后尝试编译 PDF。

支持的科目名称如下：

```text
408  数学一  数学二  英语一  英语二  政治
```

如果想先生成、不编译 PDF：

```bash
./bin/got0genpaper generate --subject "数学二"
```

如果想指定输入题库目录：

```bash
./bin/got0genpaper generate \
  --subject 408 \
  --source /path/to/exams
```

生成过程中默认允许最多一轮整卷修复。可以手动调整：

```bash
# 只生成并检查首轮结果
./bin/got0genpaper generate --subject "数学二" --max-repair-rounds 0

# 最多进行两轮整卷修复
./bin/got0genpaper generate --subject "数学二" --max-repair-rounds 2
```

完整组卷会调用模型多次，耗时和费用取决于模型服务、题目数量和修复轮数。正式生成前，建议先运行 `test-provider`。

## 生成结果在哪里

默认结果写入 `output/`：

| 文件                 | 用途                                   |
| -------------------- | -------------------------------------- |
| `paper.json`       | 结构化试卷、题目、答案、分值和评分标准 |
| `exam.tex`         | 考生试卷 LaTeX 源文件                  |
| `answers.tex`      | 参考答案和评分标准 LaTeX 源文件        |
| `answer_sheet.tex` | 答题卡 LaTeX 源文件                    |
| `exam.pdf`         | 考生试卷 PDF                           |
| `answers.pdf`      | 参考答案 PDF                           |
| `answer_sheet.pdf` | 答题卡 PDF                             |
| `spec_table.md`    | 题型、知识点、分值和难度规划           |
| `*.log`            | LaTeX 编译日志                         |

查看当前产物：

```bash
./bin/got0genpaper artifacts
```

单独重新编译已有的 LaTeX 文件：

```bash
./bin/got0genpaper compile
```

## 供应商和模型管理

项目使用 TOML 管理多个供应商，`active_id` 决定当前使用的供应商。

查看、切换和检查供应商：

```bash
./bin/got0genpaper config list
./bin/got0genpaper config show
./bin/got0genpaper config use glm-local
./bin/got0genpaper test-provider --provider glm-local
```

添加一个 OpenAI 兼容供应商：

```bash
./bin/got0genpaper config add \
  --id deepseek \
  --name DeepSeek \
  --base-url https://api.deepseek.com \
  --model deepseek-chat
```

添加后，在 `config.toml` 中补充它的 `api_key`，或者临时通过环境变量提供：

```bash
NEWAPI_API_KEY="$PAPER_LLM_KEY" \
  ./bin/got0genpaper test-provider --provider deepseek
```

也可以临时指定配置文件：

```bash
GOT0GENPAPER_CONFIG=/secure/path/paper.toml \
  ./bin/got0genpaper test
```

不同任务可以使用不同模型：

```toml
[providers.providers.model_by_type]
choice = "fast-model"
fill_blank = "balanced-model"
major = "strong-model"
```

`judge_model` 用于答案核验和文本批改，`vision_model` 用于扫描图批改，`default_model` 是通用回退模型。

## 文本答案批改（研究中）

> 当前批改链路仍在研究和验证，以下命令仅供开发测试，不建议用于正式判分。稳定的核心能力目前是预测知识点驱动的试卷生成。

准备一个 JSON 数组，`questionId` 必须对应 `output/paper.json` 中的题目 ID：

```json
[
  {"questionId":"gen_spec_001","studentOptions":["B"]},
  {"questionId":"gen_spec_017","studentAnswer":"写在这里的解题过程"}
]
```

执行批改：

```bash
./bin/got0genpaper grade \
  --paper output/paper.json \
  --answers answers.json \
  --output output/grades.json
```

批改结果会包含逐题得分、总分、满分和复核列表。缺交题会标记为 `blank`；不确定、重复或无法安全判断的答案会进入 `needs_review`，不会被强行判定。

## 扫描答题卡批改（研究中）

扫描批改使用视觉模型。程序会先处理扫描页面和答题区域，再把分割后的图像交给 `vision_model`。该流程尚未达到稳定可用状态，敬请期待后续完善。

当前命令入口如下：

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

这部分目前仍属于实验能力。定位失败、图片模糊、模型响应异常或识别不确定时，结果会标记为 `needs_review`，正式使用时必须保留人工复核。

## 常用命令速查

```bash
make build                              # 编译 CLI
make version                            # 查看版本
make test                               # 运行测试
make test-race                          # 运行竞态检测
make vet                                # 静态检查
make check                              # 格式、依赖、测试、vet、构建
make selftest                           # 检查本地运行环境
make generate SUBJECT="数学二"           # 生成试卷
make pdf                                # 编译 output/*.tex

./bin/got0genpaper help                 # 查看命令帮助
./bin/got0genpaper health                # 查看当前运行状态
./bin/got0genpaper artifacts             # 查看生成产物
```

## 出问题时先看哪里

### `storage or LLM client not initialised`

通常表示配置没有加载成功，或当前供应商缺少必要的连接参数。按下面顺序检查：

```bash
ls -l config.toml
./bin/got0genpaper config show
./bin/got0genpaper test
./bin/got0genpaper test-provider
```

如果使用了非当前目录的配置文件，确认 `GOT0GENPAPER_CONFIG` 指向正确的 TOML 文件。

### 模型连接失败

检查 `base_url` 是否是 OpenAI 兼容接口根地址、模型名称是否正确、API Key 是否有权限，并单独执行：

```bash
./bin/got0genpaper test-provider --vision
```

### PDF 编译失败

先确认引擎是否安装：

```bash
./bin/got0genpaper test
```

再查看：

```text
output/exam.log
output/answers.log
output/answer_sheet.log
```

当前版本默认依赖外部 `tectonic` 或 `xelatex`，尚未把完整 LaTeX 环境内嵌到 Go 二进制中。

## 项目结构

```text
cmd/got0genpaper/       CLI 入口和应用编排
cmd/tools/              真题检查、样例渲染和维护工具
internal/               配置、LLM、流水线、LaTeX、存储实现
data/exams/             历年真题和配图
data/syllabus/          考纲摘要及来源清单
templates/              LaTeX 模板
docs/                   使用、配置、数据、架构、开发和 CI/CD 文档
scripts/                数据抓取及辅助脚本
output/                 本地试卷、答案、答题卡和 PDF
bin/                    本地构建产物
```

`config.toml`、`output/`、`tmp/`、`bin/` 和流水线中间状态均属于本地产物，不应提交。临时目录可以随时清空。

## 许可证

项目源代码采用 [MIT License](LICENSE)，适合这个以命令行工具和工程代码为主的项目：条款简洁，允许个人使用、修改、分发和二次开发。

需要特别区分的是：`data/exams/`、`data/syllabus/` 以及其中引用或整理的历年试题、考纲和其他第三方资料，不会因为代码采用 MIT License 就自动获得 MIT 授权。使用、再分发这些资料时，应遵守各自来源的版权和使用条款；生成的试卷、答案和 PDF 也可能受到输入资料及模型服务条款的约束。

相关资料和生成内容仅供学习、研究和个人备考使用，请勿用于商业销售、付费分发、冒充官方资料，或进行未经授权的公开传播。使用者应自行确认资料来源、模型服务和目标平台的具体条款，并承担相应责任。

## 版本和发布

当前准备发布版本为 `0.1.0`，项目版本由根目录 `VERSION` 文件管理。本地构建时通过 `Makefile` 注入 CLI：

```bash
make version
```

正式发布使用 `vX.Y.Z` Git 标签。推送标签后，GitHub Actions 会自动测试代码，构建 Linux、macOS 和 Windows 版本，打包并创建 GitHub Release，同时附带 SHA-256 校验文件。

例如发布 `0.1.0`：

```bash
git add VERSION README.md LICENSE
git commit -m "release: v0.1.0"
git tag -a v0.1.0 -m "GoT0GenPaper v0.1.0"
git push origin main
git push origin v0.1.0
```

Release 页面会自动提供各平台的 `.tar.gz` 或 `.zip` 安装包。完整流程见 [`docs/CI-CD.md`](docs/CI-CD.md)。
