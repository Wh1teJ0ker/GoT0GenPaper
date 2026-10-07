# 数据与目录

## 输入资料

历年真题位于 `data/exams/csgraduates/`，按 408、数学、英语和政治分类。题库说明、覆盖年份及来源见 [`data/exams/csgraduates/README.md`](../data/exams/csgraduates/README.md)。数学一、数学二、英语一、英语二、408 和政治是 CLI 当前提供的六个组卷科目。

考纲摘要位于 `data/syllabus/`，`manifest.json` 记录适用版本、来源和核对状态。摘要不等同于官方考纲全文，使用前应确认年份与适用范围。

历年试题、考纲及第三方资料不因项目代码采用 MIT License 而自动获得 MIT 授权；使用和再分发时须遵守各自来源的条款。

## 本地流水线状态

`paths.data_dir` 保存解析结果、知识点权重、组卷蓝图和细目表等中间数据。它们可由流水线重建，不应与原始真题、考纲资料混淆。配置、缓存和生成数据不应提交到仓库。

## 生成产物

默认由 `paths.output_dir` 指定，通常是 `output/`：

- `paper.json`：结构化题目、参考答案、分值和校验所需元数据。
- `exam.tex`：可编辑的考生试卷 LaTeX。
- `exam.pdf`：编译后的试卷，仅在运行 `--compile` 或 `compile` 后产生。
- `exam.log`：PDF 编译日志。

当前生成器不创建答案册、答题卡或扫描结果。已有目录中的旧文件视为历史产物，不由新流程清理。删除或归档前应先确认实际配置的输出路径。
