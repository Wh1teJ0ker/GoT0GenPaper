# 系统结构

GoT0GenPaper 是单机命令行程序。主流程只负责生成可打印试卷：

1. 按科目加载真题语料与考纲约束。
2. 汇总知识点、题型和难度分布，生成组卷蓝图。
3. 调用配置的 LLM 生成题目及内部参考答案。
4. 校验范围、题型结构、答案、分值、难度和重复度，并按需修复题位。
5. 输出 `paper.json` 与试卷 `exam.tex`；需要时编译 `exam.pdf`。

参考答案仅留在结构化数据中供质量检查，不生成答案册。答题卡、扫描、识别和批改功能不属于当前项目。

## 模块边界

- `cmd/got0genpaper`：CLI 解析、配置管理和生成流程编排。
- `internal/config`：TOML、多供应商、路径及环境变量。
- `internal/llm`：OpenAI 兼容 API 客户端。
- `internal/pipeline/parser`：真题解析与标注。
- `internal/pipeline/gnn`、`orchestrator`：知识点统计和题位规划。
- `internal/pipeline/generator`：题面与内部参考答案生成。
- `internal/pipeline/quality`、`validator`：单题和整卷校验。
- `internal/pipeline/assembler`、`internal/latex`：试卷 LaTeX 装配、诊断和 PDF 编译。
- `internal/storage`：本地 JSON 与文本文件读写。
- `cmd/tools`、`scripts`：数据检查和维护工具，不属于日常生成主链。

`paper.json`、解析缓存和组卷蓝图用于后续复核及修复；面向打印的交付物只有试卷。PDF/PNG 扫描输入暂不处理。
