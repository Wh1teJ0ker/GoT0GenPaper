#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成《参考答案与评分标准》教师卷 PDF。

读取 dist 包中的 output/paper.json 与 output/exam.tex 的导言区，
输出 output/answers.tex 并调用 tectonic 编译为 output/answers.pdf。

用法：
    python3 scripts/gen_answers_pdf.py math2_20261011_232710 [408_20261011_222726 ...]
    python3 scripts/gen_answers_pdf.py --all
"""

import json
import os
import re
import subprocess
import sys

DIST = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "dist")

CJK_PUNCT = "（），。；：、！？"
MATH_RE = re.compile(r"\$\$.*?\$\$|\$[^$]*\$", re.S)
CMD_RE = re.compile(
    r"\\begin\{[a-zA-Z*]+\}"
    r"|\\end\{[a-zA-Z*]+\}"
    r"|\\[a-zA-Z]+\*?\{[^{}]*\}"
    r"|\\[a-zA-Z]+"
    r"|\\[^a-zA-Z]"
)
TABLE_LINE = re.compile(r"^\s*\|.*\|\s*$")
TABLE_SEP = re.compile(r"^\s*\|[\s:|\-]+\|\s*$")

RAW_CHAR_MAP = {
    "\u201c": r"\textquotedblleft ",
    "\u201d": r"\textquotedblright ",
    "\u2018": r"\textquoteleft ",
    "\u2019": r"\textquoteright ",
    "\u2013": "--",
    "\u2014": "---",
    "\u2026": r"\ldots{}",
}


# ---------------------------------------------------------------- LaTeX 渲染
def _protect(seg):
    store = []

    def repl(match):
        store.append(match.group(0))
        return "\x00%d\x00" % (len(store) - 1)

    return CMD_RE.sub(repl, seg), store


def _restore(seg, store):
    for i, token in enumerate(store):
        seg = seg.replace("\x00%d\x00" % i, token)
    return seg


def escape_inline(seg, bold=True, paragraphs=False):
    """把一段纯文本（不含数学）安全转换为 LaTeX。"""
    for raw, repl in RAW_CHAR_MAP.items():
        seg = seg.replace(raw, repl)
    seg, store = _protect(seg)
    seg = re.sub(r"`([^`\n]+)`", lambda m: "\\texttt{" + m.group(1) + "}", seg)
    seg = seg.replace("&", r"\&").replace("%", r"\%").replace("#", r"\#")
    seg = seg.replace("_", r"\_")
    seg = seg.replace("^", r"\textasciicircum{}")
    seg = seg.replace("~", r"\textasciitilde{}")
    if bold:
        seg = re.sub(r"\*\*([^*\n]+?)\*\*", r"\\textbf{\1}", seg)
    seg = _restore(seg, store)
    if paragraphs:
        seg = re.sub(r"(?m)^[ \t]*[-\u2013]\s+", "\\\\textbullet{} ", seg)
        seg = re.sub(r"[ \t]*\n[ \t]*", "\n\n", seg)
        seg = re.sub(r"\n{3,}", "\n\n", seg)
    return seg


ASCII_PUNCT = {"。": ".", "，": ",", "、": ",", "；": ";", "：": ":", "？": "?", "！": "!", "（": "(", "）": ")"}


def fix_math_punct(seg):
    """处理数学段首尾的中文标点。

    行间公式（$$...$$）尾部的句号改写成数学模式内的英文句点，与试卷 PDF 的
    处理方式一致；行内公式（$...$）的标点则移到数学模式之外，避免出现孤立的
    标点行。
    """
    display = seg.startswith("$$")
    opener = "$$" if display else "$"
    inner = seg[2:-2] if display else seg[1:-1]
    lead = ""
    m = re.match(r"^[" + CJK_PUNCT + r"]+", inner)
    if m:
        lead, inner = m.group(0), inner[len(m.group(0)):]
    trail = ""
    m = re.search(r"[" + CJK_PUNCT + r"]+$", inner)
    if m:
        trail, inner = m.group(0), inner[:m.start()]
    if not inner.strip():
        return lead + trail
    if display:
        inner += "".join(ASCII_PUNCT[c] for c in trail)
        trail = ""
    return lead + opener + inner + opener + trail


def table_latex(rows):
    ncol = max(len(r) for r in rows)
    spec = "|" + "c|" * ncol
    body = []
    for k, row in enumerate(rows):
        row = row + [""] * (ncol - len(row))
        body.append(" & ".join(escape_inline(c, bold=False) for c in row) + r" \\")
        if k == 0:
            body.append(r"\hline")
    return (
        "\n\\begin{center}\n\\begin{tabular}{%s}\n\\hline\n%s\n\\hline\n\\end{tabular}\n\\end{center}\n"
        % (spec, "\n".join(body))
    )


def extract_tables(text):
    lines = text.split("\n")
    out, tables = [], []
    i = 0
    while i < len(lines):
        if TABLE_LINE.match(lines[i]):
            j = i
            while j < len(lines) and TABLE_LINE.match(lines[j]):
                j += 1
            rows = []
            for ln in lines[i:j]:
                if TABLE_SEP.match(ln):
                    continue
                rows.append([c.strip() for c in ln.strip().strip("|").split("|")])
            tables.append(table_latex(rows))
            out.append("\x01TABLE%d\x01" % (len(tables) - 1))
            i = j
        else:
            out.append(lines[i])
            i += 1
    return "\n".join(out), tables


def render(text, bold=True):
    """渲染含数学段落的混合文本。"""
    text, tables = extract_tables(text)
    out, pos = [], 0
    for m in MATH_RE.finditer(text):
        out.append(escape_inline(text[pos:m.start()], bold, paragraphs=True))
        out.append(fix_math_punct(m.group(0)))
        pos = m.end()
    out.append(escape_inline(text[pos:], bold, paragraphs=True))
    s = "".join(out)
    for i, tbl in enumerate(tables):
        s = s.replace("\x01TABLE%d\x01" % i, tbl)
    return s


# ---------------------------------------------------------------- 答案拆分
HEAD_RE = re.compile(r"\*{0,2}【?答案】?\*{0,2}\s*[：:]\s*")
NON_ANSWER_HEADS = ("考查目标", "答案与解析", "解析", "【参考范文】", "【评分依据】", "【检验】")


def split_answer(text, threshold=170):
    """返回 (head, rest)。head 为参考答案摘要，可为 None。"""
    t = text.strip()
    head, rest = None, t
    body = t
    m = HEAD_RE.match(t)
    if m:
        body = t[m.end():]
    lm = re.match(r"^([A-G])([。.．，,]?)(.*)$", body, re.S)
    # 排除 "答案：Breakfast…" 这类以字母开头的整词，但保留 "答案：A。A 项：…"
    if lm and lm.group(2) and not re.match(r"[A-Za-z][A-Za-z]", body[2:4]):
        return lm.group(1), lm.group(3).lstrip("\n ").strip()
    if lm and lm.group(3)[:1] in ("\n", "") and body[1:2] in ("", "\n"):
        return lm.group(1), lm.group(3).lstrip("\n ").strip()
    # 非字母答案：取首个非空行作为参考答案
    lines = body.split("\n")
    for k, ln in enumerate(lines):
        if ln.strip():
            candidate = ln.strip().strip("*").strip()
            if candidate.startswith(NON_ANSWER_HEADS):
                break
            if len(candidate) <= threshold:
                head = candidate
                rest = "\n".join(lines[k + 1:]).strip()
            break
    if not m:
        head = None
        rest = t
    return head, rest


# ---------------------------------------------------------------- 逐包配置
RUBRICS = {
    "math2": {
        # ---------------- 数学二 ----------------
        "spec_017": [
            (r"第 (1) 问：正确拆分为 $x\int_0^xf(t)\,dt-\int_0^xtf(t)\,dt$ 并求导，得 $F'(x)=\int_0^xf(t)\,dt$、$F''(x)=f(x)$", 6),
            (r"第 (2) 问：由 $f(t)-1=2t+o(t)$ 正确转化（换元或洛必达法则），求得极限值 $\frac13$", 5),
        ],
        "spec_018": [
            (r"求出内部驻点 $(-1,-1)$ 并判断其不在区域 $D$ 内", 3),
            (r"用拉格朗日乘数法求出边界圆周上的全部候选点（漏解一组候选点扣 2 分）", 5),
            (r"比较各候选点函数值，得出最大值 $2$、最小值 $\frac12-\sqrt2$", 3),
        ],
        "spec_019": [
            (r"第 (1) 问：由通解读出二重特征根 $-2$，求得 $a=4,\ b=4$", 4),
            (r"第 (2) 问：由初值条件求出特解 $y=(1+2x)e^{-2x}$", 4),
            (r"第 (2) 问：判断单调性并求出最大值 $y(0)=1$", 4),
        ],
        "spec_020": [
            (r"第 (I) 问：由定义求出 $f_x(0,0)=f_y(0,0)=1$", 3),
            (r"第 (II) 问：写出残差表达式 $-\frac{hk(h+k)}{h^2+k^2}$，并用两条路径论证残差比极限不存在，从而不可微", 5),
            (r"第 (III) 问：判断 $f_x$ 在 $(0,0)$ 处不连续并说明理由；求出 $\mathrm{d}z\big|_{(1,0)}=2\,\mathrm{d}x$", 4),
        ],
        "spec_021": [
            (r"第 (Ⅰ) 问：构造 $F(x,y,z)$ 并用隐函数求导公式得 $z_x=\frac{x}{2-z}$、$z_y=\frac{y}{2-z}$", 6),
            (r"第 (Ⅱ) 问：解出 $z=2\pm\sqrt2$ 两支（漏一支扣 1.5 分）", 3),
            (r"第 (Ⅱ) 问：分别求出两支全微分并写成 $\pm$ 形式", 3),
        ],
        "spec_022": [
            (r"第 (I) 问：列出系数方程组并计算行列式 $1+k^3$，讨论得 $k\neq-1$ 时线性无关", 3),
            (r"第 (I) 问：得出 $k=-1$ 时线性相关及非零关系式 $\beta_1+\beta_2+\beta_3=0$", 3),
            (r"第 (II) 问：计算行列式 $t^2+t-1$ 并讨论相关性", 3),
            (r"第 (II) 问：相关时给出极大线性无关组及 $\gamma_3=t\gamma_1+(1-t)\gamma_2$", 3),
        ],
        },
        "408": {
        # ---------------- 408 ----------------
        "spec_041": [
            (r"第一趟（按个位）分配与收集正确：$\langle720,355,436,457,657,329,839\rangle$", 4),
            (r"第二趟（按十位）分配与收集正确：$\langle720,329,436,839,355,457,657\rangle$", 4),
            (r"第三趟（按百位）分配与收集正确，最终序列完全有序", 5),
        ],
        "spec_042": [
            (r"第 (1) 问：插入与分裂过程正确（$50$、$5$ 两次分裂）", 2),
            (r"第 (1) 问：最终根结点 $[20,35]$ 与三个叶结点 $[5,10,15]$、$[20,25,30]$、$[35,45,50]$ 正确", 3),
            (r"第 (1) 问：叶链正确", 1),
            (r"第 (2) 问：查找 $28$ 的比较序列 $20,35,20,25,30$ 正确", 2),
            (r"第 (2) 问：查找 $15$ 的访问路径正确，并说明无需沿叶链移动", 2),
        ],
        "spec_043": [
            (r"第 (1) 问：可用容量 $8\,\mathrm{TiB}$，并说明校验信息合计占用相当于一块盘的容量", 3),
            (r"第 (2) 问：每个完整条带含 $4$ 个数据条带单元、共 $256\,\mathrm{KiB}$", 2),
            (r"第 (2) 问：完整条带数 $33\,554\,432$", 2),
            (r"第 (3) 问：结论“能”", 1),
            (r"第 (3) 问：说明分布式校验可由其余盘重建缺失数据、阵列降级继续服务", 2),
        ],
        "spec_044": [
            (r"第 (1) 问：$T_{\min}=15+45+15+5=80\,\mathrm{ns}$", 4),
            (r"第 (1) 问：最高时钟频率 $12.5\,\mathrm{MHz}$", 2),
            (r"第 (1) 问：理论最大传输率 $50\,\mathrm{MB/s}$", 2),
            (r"第 (2) 问：$50\,\mathrm{MHz}$ 对应周期 $20\,\mathrm{ns}$", 2),
            (r"第 (2) 问：稳态最大传输率 $200\,\mathrm{MB/s}$", 3),
        ],
        "spec_045": [
            (r"第 (1) 问：$IR$ 为瞬态寄存器，返回时按恢复的 $PC$ 重新取指，无需保存", 2),
            (r"第 (2) 问：不冗余；不恢复 $Base/Limit$ 会使返回用户态后沿用内核态地址映射、丧失越界保护", 2),
            (r"第 (3) 问：嵌套中断覆盖 $B_{PSW}/B_{PC}$ 导致最终无法正确返回用户态的严重后果", 2),
            (r"第 (3) 问：完善机制：进入异常时把备份寄存器内容压栈保存", 1),
        ],
        "spec_046": [
            (r"第 (1) 问：每个柱面 $6\times120=720$ 个磁盘块", 1),
            (r"第 (1) 问：柱面号 $7$（$5432=7\times720+392$）", 2),
            (r"第 (1) 问：磁头号 $3$、扇区号 $33$（$392=3\times120+32$）", 2),
            (r"第 (2) 问：设备独立性软件提供统一接口并完成逻辑块到 LBA 的映射", 2),
            (r"第 (2) 问：设备驱动程序完成 LBA 到 CHS 转换、检查设备参数并发出读命令", 1),
        ],
        "spec_047": [
            (r"第 (1) 问：目的 IP $198.51.100.1$、目的端口 $60001$", 2),
            (r"第 (2) 问：转换后源 $203.0.113.10:20$、目的 $192.168.1.100:50000$", 2),
            (r"第 (3) 问：报文段数 $N=\lceil1228800/1000\rceil=1229$", 1),
            (r"第 (3) 问：单个完整停等周期 $20.93\,\mathrm{ms}$", 2),
            (r"第 (3) 问：总时间 $1228\times20.93+10.729\approx25.713\,\mathrm{s}$", 2),
        ],
        },
        "english2": {
        # ---------------- 英语二 ----------------
        "spec_046": [
            ("信息完整：关键信息点齐全（技术非万能、短期缓解、利弊权衡、公众参与、欧洲城镇自行车道实例、决定性因素），重大遗漏一处扣 1-2 分", 6),
            ("逻辑衔接：转折（然而）、因果（这正是……的原因）、总结（换言之）等逻辑关系准确译出，行文连贯", 4),
            ("语言表达：用词准确、无翻译腔，重大误译一处扣 1-2 分", 5),
        ],
        "spec_047": [
            ("内容要点：邀请目的、活动背景、演讲主题、时间地点、礼貌结尾齐全，每缺一个要点扣 1 分", 4),
            ("语言表达：语法正确、用词得体，语气正式礼貌", 4),
            ("格式与语域：称呼与落款规范；词数明显不足（少于 70 词）酌扣 1-2 分", 2),
        ],
        "spec_048": [
            ("内容：准确描述三类阅读媒介的变化趋势并给出合理评论，数据解读错误一处扣 1 分", 5),
            ("结构：图表描述-原因分析-个人评论层次清晰，衔接自然", 4),
            ("语言：语法与用词准确、句式多样；词数明显不足（少于 110 词）酌扣 1-2 分", 6),
        ],
        },
}

HEAD_OVERRIDES = {
    "spec_046@english2": "参考译文如下。",
    "spec_048@english2": "参考范文如下（满分作文示例）。",
}

FILL_RUBRIC = "答案正确得 {score} 分；与参考答案等价的正确形式同样得分；否则不得分。"
CHOICE_RUBRIC = "选对得 {score} 分；错选、多选或不选得 0 分。"

PACKAGES = {
    "math2_20261011_232710": {
        "sections": [
            ("一、单项选择题（第 1-10 题，每题 5 分，共 50 分）", 1, 10, "choice"),
            ("二、填空题（第 11-16 题，每题 5 分，共 30 分）", 11, 16, "fill"),
            ("三、解答题（第 17-22 题，共 70 分）", 17, 22, "major"),
        ],
    },
    "408_20261011_222726": {
        "sections": [
            ("一、单项选择题（第 1-40 题，每题 2 分，共 80 分）", 1, 40, "choice"),
            ("二、综合应用题（第 41-47 题，共 70 分）", 41, 47, "major"),
        ],
    },
    "english2_20261012_110736": {
        "sections": [
            ("一、完形填空（第 1-20 题，每题 0.5 分，共 10 分）", 1, 20, "choice"),
            ("二、阅读理解 Part A（第 21-40 题，每题 2 分，共 40 分）", 21, 40, "choice"),
            ("三、阅读理解 Part B 新题型（第 41-45 题，每题 2 分，共 10 分）", 41, 45, "choice"),
            ("四、翻译（第 46 题，共 15 分）", 46, 46, "major"),
            ("五、写作 Part A 应用文（第 47 题，共 10 分）", 47, 47, "major"),
            ("六、写作 Part B 图表作文（第 48 题，共 15 分）", 48, 48, "major"),
        ],
    },
}

SUBJECT_TAG = {"数学二": "math2", "408": "408", "英语二": "english2"}

EXPLAIN_NOTE = {
    "math2": "客观题（单项选择、填空）：答案唯一或等价即得分。解答题：按各题评分标准分步给分；"
    "步骤正确而结果有误者按步给分，结果正确但缺少关键推理者酌减。",
    "408": "单项选择题：选对得满分，错选、多选或不选得 0 分。综合应用题：按要点分步给分；"
    "考生推导过程与参考答案不同但结论正确的，参照给分点酌情给分。",
    "english2": "客观题（完形填空、阅读理解、新题型）：选对得满分，错选、多选或不选得 0 分。"
    "翻译与写作：按内容、逻辑与语言三方面综合评分；拼写与标点错误酌情扣分，词数明显不足酌扣。",
}


def fmt_score(score):
    if float(score) == int(score):
        return str(int(score))
    return ("%g" % score)


def speed_table(rows, per_row=10):
    lines = [r"\begin{center}", r"\begin{tabular}{|" + "c|" * (per_row + 1) + "}", r"\hline"]
    for start in range(0, len(rows), per_row):
        chunk = rows[start:start + per_row]
        pad = per_row - len(chunk)
        nums = ["题号"] + [str(n) for n, _ in chunk] + [""] * pad
        ans = ["答案"] + [a for _, a in chunk] + [""] * pad
        lines.append(" & ".join(nums) + r" \\ \hline")
        lines.append(" & ".join(ans) + r" \\ \hline")
    lines += [r"\end{tabular}", r"\end{center}"]
    return "\n".join(lines)


def question_latex(number, q, kind, pkg_tag):
    score = fmt_score(q["score"])
    out = [r"\needspace{4\baselineskip}"]
    head, rest = split_answer(q["answer"])
    override = HEAD_OVERRIDES.get("%s@%s" % (q["specId"], pkg_tag))
    if override:
        head = override
    if kind == "major":
        out.append(r"\noindent{\bfseries %d．}（%s 分）" % (number, score))
        out.append("")
        stem = re.sub(r"^[（(]本题满分\s*[\d.]+\s*分[）)]\s*", "", q["stem"].strip())
        out.append(r"{\bfseries 【题目】}" + render(stem))
        if head:
            out.append("")
            out.append(r"{\bfseries 【参考答案】}" + render(head, bold=False))
        if rest:
            if not head:
                rest = re.sub(
                    r"^[（(【]*(答案与解析|考查目标与判据|答案|解析)[）)】]*\s*[：:]\s*",
                    "",
                    rest,
                    count=1,
                )
            out.append("")
            label = "" if head else r"{\bfseries 【参考答案与解析】}"
            out.append(label + render(rest))
        kps = [
            p for p in q.get("points", [])
            if not p.startswith("难度目标候选")
            and not re.fullmatch(r"题位\s*\d+", p)
            and p not in ("408", "数学二", "英语二", "math2", "english2")
        ]
        if kps:
            out.append("")
            out.append(r"{\bfseries 【知识点】}" + render(" · ".join(kps), bold=False))
        items = RUBRICS[pkg_tag].get(q["specId"])
        if items is None:
            raise SystemExit("缺少评分标准：%s/%s" % (pkg_tag, q["specId"]))
        total = sum(p for _, p in items)
        if abs(total - float(q["score"])) > 1e-6:
            raise SystemExit(
                "评分标准分值不符：%s/%s 合计 %s，题目分值 %s"
                % (pkg_tag, q["specId"], total, q["score"])
            )
        out.append("")
        out.append(r"{\bfseries 【评分标准】}")
        out.append(r"\begin{itemize}")
        for text, pts in items:
            out.append(r"\item " + render(text, bold=False) + "（%s 分）" % fmt_score(pts))
        out.append(r"\end{itemize}")
    else:
        shown = render(head, bold=False) if head else "见解析"
        out.append(
            r"\noindent{\bfseries %d．}（%s 分）{\bfseries 【答案】}%s" % (number, score, shown)
        )
        if rest:
            out.append("")
            out.append(render(rest))
        template = FILL_RUBRIC if kind == "fill" else CHOICE_RUBRIC
        out.append("")
        out.append(r"{\bfseries 【评分标准】}" + template.format(score=score))
    return "\n".join(out) + "\n"


def build_document(pkg, cfg, exam_tex):
    paper_path = os.path.join(DIST, pkg, "output", "paper.json")
    paper = json.load(open(paper_path, encoding="utf-8"))
    questions = paper["questions"]
    subject = paper["subject"]
    pkg_tag = SUBJECT_TAG[subject]

    pre = exam_tex.split(r"\begin{document}")[0]
    pre = pre.replace(
        r"\fancyfoot[L]{\zihao{5}}",
        r"\fancyfoot[L]{\zihao{5}参考答案与评分标准（教师卷）}",
    )

    body = [
        r"\begin{center}",
        r"{\zihao{2}\bfseries %s 参考答案与评分标准}\\[0.6em]" % subject,
        r"{\zihao{4}教师用卷 · 全卷满分 %s 分 · 共 %d 题}\\[0.4em]"
        % (fmt_score(paper["totalScore"]), len(questions)),
        r"{\zihao{5}试卷包：%s}" % render(pkg, bold=False).strip(),
        r"\end{center}",
        "",
        r"{\zihao{4}\bfseries 评分标准说明}",
        "",
        EXPLAIN_NOTE[pkg_tag],
        "",
    ]

    # 客观题答案速查表
    speed_rows = []
    for i, q in enumerate(questions, 1):
        if q["type"] == "major":
            continue
        head, _ = split_answer(q["answer"])
        if head and re.fullmatch(r"[A-G]", head):
            speed_rows.append((i, head))
    if speed_rows:
        body.append(r"{\zihao{4}\bfseries 客观题答案速查表}")
        body.append("")
        body.append(speed_table(speed_rows))
        body.append("")

    for title, start, end, kind in cfg["sections"]:
        body.append(r"\vspace{0.4em}")
        body.append(r"{\zihao{4}\bfseries %s}" % title)
        body.append("")
        for number in range(start, end + 1):
            body.append(question_latex(number, questions[number - 1], kind, pkg_tag))

    body.append(r"\end{document}")
    return pre + r"\begin{document}" + "\n" + "\n".join(body) + "\n"


def build_package(pkg):
    cfg = PACKAGES[pkg]
    outdir = os.path.join(DIST, pkg, "output")
    exam_tex = open(os.path.join(outdir, "exam.tex"), encoding="utf-8").read()
    tex = build_document(pkg, cfg, exam_tex)
    tex_path = os.path.join(outdir, "answers.tex")
    with open(tex_path, "w", encoding="utf-8") as fh:
        fh.write(tex)
    print("生成", tex_path)
    result = subprocess.run(
        ["tectonic", "-o", outdir, tex_path],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        sys.stderr.write(result.stdout[-4000:])
        sys.stderr.write(result.stderr[-4000:])
        raise SystemExit("编译失败：%s" % pkg)
    pdf_path = os.path.join(outdir, "answers.pdf")
    print("编译成功", pdf_path, os.path.getsize(pdf_path), "bytes")
    return pdf_path


def main():
    args = sys.argv[1:]
    if not args or args == ["--all"]:
        targets = list(PACKAGES)
    else:
        targets = args
    for pkg in targets:
        if pkg not in PACKAGES:
            raise SystemExit("未知试卷包：%s" % pkg)
        build_package(pkg)


if __name__ == "__main__":
    main()
