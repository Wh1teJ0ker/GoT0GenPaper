#!/usr/bin/env bash
# 编译产出目录中的考生试卷为 PDF。
# 用法: scripts/build_pdf.sh [output_dir]   (默认 output/)
# 引擎: 优先 xelatex (MacTeX/TeX Live), 回退 tectonic。
set -euo pipefail

DIR="${1:-output}"

if ! command -v xelatex >/dev/null 2>&1 && ! command -v tectonic >/dev/null 2>&1; then
  echo "错误: 未找到 xelatex 或 tectonic。安装: brew install --cask basictex 或 brew install tectonic" >&2
  exit 1
fi

tex=exam.tex
if [ ! -f "$DIR/$tex" ]; then
  echo "错误: $DIR/$tex 不存在, 请先生成试卷" >&2
  exit 1
fi
echo "编译 $tex ..."
if command -v xelatex >/dev/null 2>&1; then
  (cd "$DIR" && xelatex -interaction=nonstopmode -halt-on-error "$tex" >/dev/null && \
   xelatex -interaction=nonstopmode -halt-on-error "$tex" >/dev/null)
else
  tectonic -o "$DIR" "$DIR/$tex"
fi
echo "✓ $DIR/${tex%.tex}.pdf"
