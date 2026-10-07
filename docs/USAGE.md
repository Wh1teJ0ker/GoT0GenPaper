# 使用说明

当前程序只生成考生试卷。参考答案保留在 `paper.json` 供内部质量校验；不输出答案册或答题卡，也不提供扫描和批改命令。

## 1. 环境准备

- Go 1.25 或更新版本，用于构建 CLI。
- 一个兼容 OpenAI Chat Completions API 的 LLM 服务，用于生成题目。
- `tectonic` 或 `xelatex`，仅在需要 PDF 时安装。

macOS 可运行 `brew install go tectonic`。尚未安装 TeX 引擎时，仍可生成 `paper.json` 与 `exam.tex`。

## 2. 配置与自检

```bash
cp config.example.toml config.toml
chmod 600 config.toml
# 编辑 base_url、default_model 和 api_key
make build
./bin/got0genpaper version
./bin/got0genpaper test
```

`test` 只检查本地依赖、配置、科目题库、模板和输出目录权限；不会调用模型或生成试卷。要测试真实供应商连接，显式运行：

```bash
./bin/got0genpaper test-provider
./bin/got0genpaper test-provider --provider glm-local
./bin/got0genpaper test --network
```

这些命令会发出轻量模型请求并可能产生少量费用。多供应商管理见[配置说明](CONFIGURATION.md)。

## 3. 生成试卷

支持 `408`、`数学一`、`数学二`、`英语一`、`英语二` 和 `政治`。默认从配置 `data_dir` 下对应的真题目录读取语料。

```bash
# 生成 408，输出 paper.json 与 exam.tex
./bin/got0genpaper generate --subject 408

# 生成数学二并编译 exam.pdf
./bin/got0genpaper generate --subject "数学二" --compile

# 指定来源文件或目录
./bin/got0genpaper generate --subject 408 --source /path/to/exams
```

每次完整生成会多次请求模型。正式生成前先执行 `test-provider` 确认连接、密钥和模型权限。

## 4. 控制难度

```bash
# 使用难度预设
./bin/got0genpaper generate --subject "数学二" --difficulty hard

# 指定易、中、难比例，三项之和为 1
./bin/got0genpaper generate --subject "数学二" --difficulty-hist 0.2,0.6,0.2

# 数学二共 22 题，精确指定难度题数
./bin/got0genpaper generate --subject "数学二" --difficulty-counts 2,10,10
```

`--difficulty` 可选 `default`、`easy`、`medium`、`hard`。自定义比例按最大余数法换算为整数题数；也可用 `easy=2,medium=10,hard=10` 的命名格式。比例和题数不能同时指定，也不能与非默认难度预设同时指定。除 `default` 外的预设会作为严格题数目标。

默认最多进行一轮整卷修复。使用 `--max-repair-rounds 0` 只生成并校验首轮结果；也可以指定更大的非负轮数。修复会再次调用模型并产生费用。

## 5. 校验与修复

生成过程会检查题型结构、考纲范围、题目完整性、参考答案、分值、难度和题目重复。单独检查已有试卷不会重新生成：

```bash
./bin/got0genpaper validate
./bin/got0genpaper validate --paper output/archive/run/paper.json --data output/archive/run/data
```

只重装配现有 `paper.json`，不调用模型：

```bash
./bin/got0genpaper reassemble --paper output/archive/run/paper.json --compile
```

根据校验报告修复全部问题题位，或显式指定题位：

```bash
./bin/got0genpaper repair
./bin/got0genpaper repair --specs spec_031,spec_037 --compile
```

指定归档 `paper.json` 时可附上其对应的 `--data` 缓存目录，避免覆盖当前 `output/`。修复会重新生成指定题位，其余题目保持不变。

## 6. 查看与编译

```bash
./bin/got0genpaper artifacts
./bin/got0genpaper compile
```

默认输出目录由 `paths.output_dir` 决定：

- `paper.json`：结构化题目、内部参考答案、分值及校验元数据。
- `exam.tex`：可编辑的考生试卷。
- `exam.pdf`：编译后的考生试卷。
- `exam.log`：编译日志。

`compile` 只编译 `exam.tex`。旧版输出目录中可能留有历史答案册或答题卡文件；新生成流程不会创建或清理这些文件。

## 7. 常见问题

连接失败时检查 active provider 的 `base_url`、模型名、API Key 和模型权限，然后单独运行 `test-provider`。使用其他 TOML 配置时设置 `GOT0GENPAPER_CONFIG=/path/to/config.toml`。

PDF 编译失败时运行 `got0genpaper test` 确认 PDF 引擎状态，并检查 `output/exam.log`。如果只需要 LaTeX 源文件，不要求本地安装 TeX 引擎。

生成内容属于模型生成的模拟材料，不是官方试题或标准答案。请自行核对题目范围和答案；历年真题、考纲及生成内容的使用还应遵守来源和模型服务条款。
