# 维护工具

日常使用优先运行 `got0genpaper`。辅助程序与抓取脚本面向开发、人工审计和数据更新；部分命令会写入 `data/` 或 `output/`，运行前确认目标路径。

## `cmd/tools`

```bash
go run ./cmd/tools/parse_exam [真题.md]
go run ./cmd/tools/check_exams
go run ./cmd/tools/inspect_scores
go run ./cmd/tools/render_sample [真题.md]
```

- `parse_exam`：解析单份真题并打印题目概要。
- `check_exams`：批量解析内置真题，汇总题数、分值及缺失字段。
- `inspect_scores`：输出若干历史数学卷的题型和分值诊断。
- `render_sample`：以真实 408 真题装配版式样例，写入 `output/render_sample/`，若有 TeX 引擎则编译 PDF。

## `scripts`

- `scrape_csgraduates.py`：重新抓取并转换真题；依赖 `beautifulsoup4`、`markdownify`，会联网并写入 `data/exams/csgraduates/`。请先备份/审阅差异，避免覆盖人工修订。
- `fetch_syllabus_sources.sh [目录]`：下载公开考纲来源到指定临时目录；不传目录时新建临时目录。下载文件仅用于核对，项目只保留摘要和来源清单。
- `build_pdf.sh [目录]`：只编译目录中的 `exam.tex`。常规 `output/` 建议使用 `got0genpaper compile`，它会先校验试卷并报告 LaTeX 诊断。

数学二的一次性重组与修复脚本已从工具集移除；日常试卷统一通过 `got0genpaper generate` 生成，避免绕过考纲、校验和输出约束。
