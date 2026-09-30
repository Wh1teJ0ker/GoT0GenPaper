#!/usr/bin/env bash
set -euo pipefail

# Fetch public source pages/PDFs into a temporary directory for review. The
# repository stores only the compact summaries and provenance manifest because
# some source PDFs are copyrighted and the annual bundles are very large.
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TMP_DIR="${1:-$(mktemp -d)}"
mkdir -p "$TMP_DIR"

fetch() {
  local name="$1"
  local url="$2"
  curl --http1.1 -L --retry 2 --max-time 60 -sS -A 'Mozilla/5.0' "$url" -o "$TMP_DIR/$name"
  printf '%s\t%s\n' "$name" "$url"
}

fetch 408-2026.pdf 'https://www.kmxwd.com/uploads/images/20251015/68eef5daf0564.pdf'
fetch 408-school-page.html 'https://yjsglxt.scau.edu.cn/open/Wechat/SsZsZyml/Kskm.aspx?kskmdm=408&kskmmc=408%7C%E8%AE%A1%E7%AE%97%E6%9C%BA%E5%AD%A6%E7%A7%91%E4%B8%93%E4%B8%9A%E5%9F%BA%E7%A1%80&nd=2026'
fetch math-2026.html 'https://jingweimath.com/syllabus.html'
fetch english-2026.html 'https://zhenti99.com/dagang/%E8%80%83%E7%A0%94%E8%8B%B1%E8%AF%AD/'
fetch politics-2027.html 'https://www.hqwx.com/kaoyan-kaoshi/ziliaolm/1449220.html'
fetch politics-2027.pdf 'https://oss-hqwx-video.hqwx.com/%E8%80%83%E7%A0%94%E6%94%BF%E6%B2%BB%E5%A4%A7%E7%BA%B2%EF%BC%882027%E5%B9%B4%EF%BC%89_9f9ef4ca12b458712ec93e951df44f04e4edbe12.pdf'
fetch english1-2027.pdf 'https://oss-hqwx-video.hqwx.com/%E8%80%83%E7%A0%94%E8%8B%B1%E8%AF%AD%E5%A4%A7%E7%BA%B2-%E8%8B%B1%E8%AF%AD%E4%B8%80%EF%BC%882027%E5%B9%B4%EF%BC%89_a541f804ebddb64fd99740758be929ccc7f1f30c.pdf'
fetch english2-2027.pdf 'https://oss-hqwx-video.hqwx.com/%E8%80%83%E7%A0%94%E8%8B%B1%E8%AF%AD%E5%A4%A7%E7%BA%B2-%E8%8B%B1%E8%AF%AD%E4%BA%8C%EF%BC%882027%E5%B9%B4%EF%BC%89_34baf4c51639a892977b4054e65ccd0fb77e32b7.pdf'

printf 'Saved crawl artifacts in %s\n' "$TMP_DIR"
printf 'Summaries remain in %s/data/syllabus and should be reviewed before replacing them.\n' "$ROOT_DIR"
