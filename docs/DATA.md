# 数据与目录

## 固定输入

历年真题按科目放在 `data/exams/csgraduates/`：

```text
408/
math/math1/  math/math2/  math/math3/  math/math_old/
english/english1/  english/english2/
politics/
assets/
```

题库说明、语料格式、覆盖年份和来源见 [`data/exams/csgraduates/README.md`](../data/exams/csgraduates/README.md)。数学一、数学二、英语一、英语二、408 和政治是 CLI 的六个可选组卷科目；数学三与早年数学卷保留为题库/研究数据，不代表当前六科组卷接口支持它们。

考纲摘要位于 `data/syllabus/`，`manifest.json` 记录适用版本、来源和状态。摘要不是官方出版物全文；年份变化时需重新核对。政治及英语科目还存在年度内容更新边界，详细状态见该目录 README 与各科文件。

## 本地流水线状态

运行中间数据保存在 `data/` 顶层 JSON，例如解析结果、知识点权重、组卷蓝图、题目和审核结果。它们属于本地工作状态，不是原始题库；正常组卷可由流水线重新生成。请勿与 `data/exams/`、`data/syllabus/` 混淆，也不要在提交中放入个人生成结果。

## 输出

默认输出目录由 TOML 的 `paths.output_dir` 指定，通常为 `output/`：

- `paper.json`：试卷题目、答案及评分标准。
- `exam.tex`、`answers.tex`、`answer_sheet.tex`：LaTeX 源码。
- 同名 `.pdf`：编译后的试卷、答案和答题卡。
- `spec_table.md`：双向细目表。
- `scan.json`、`grades-scan.json`：显式指定 `--output` 时的扫描和批改报告。
- `segments/`：显式指定 `--segments-dir` 时的答题区域图像。

`output/` 和 `tmp/` 是本地产物目录并被 Git 忽略。真题图片是题库输入，应保留；清理本地产物前先确认配置中的真实输出路径。
