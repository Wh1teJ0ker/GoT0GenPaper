#!/usr/bin/env bash
# 编译产出目录下的 LaTeX 三件套为 PDF (试卷/答案/答题卡)。
# 用法: scripts/build_pdf.sh [output_dir]   (默认 output/)
# 引擎: 优先 xelatex (MacTeX/TeX Live), 回退 tectonic。
set -euo pipefail

DIR="${1:-output}"

if ! command -v xelatex >/dev/null 2>&1 && ! command -v tectonic >/dev/null 2>&1; then
  echo "错误: 未找到 xelatex 或 tectonic。安装: brew install --cask basictex 或 brew install tectonic" >&2
  exit 1
fi

for tex in exam.tex answers.tex answer_sheet.tex; do
  if [ ! -f "$DIR/$tex" ]; then
    echo "跳过 $DIR/$tex (不存在, 请先运行装配)"
    continue
  fi
  echo "编译 $tex ..."
  if command -v xelatex >/dev/null 2>&1; then
    (cd "$DIR" && xelatex -interaction=nonstopmode -halt-on-error "$tex" >/dev/null && \
     xelatex -interaction=nonstopmode -halt-on-error "$tex" >/dev/null)
  else
    tectonic -o "$DIR" "$DIR/$tex"
  fi
  echo "✓ $DIR/${tex%.tex}.pdf"
done
