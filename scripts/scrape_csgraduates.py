#!/usr/bin/env python3
"""Scrape past exam papers (408/数学/英语/政治) from csgraduates.com into Markdown.

- Reads leaf URLs from the site sitemap (or /tmp/leaves.txt if present).
- Raw HTML cached under CACHE_DIR; Markdown + assets written under OUT_DIR.
- Choice questions carry data-answer / data-tags attributes which are
  converted to 【答案】/【标签】 lines; essay solutions become 【解析】 blocks.
- KaTeX HTML (no MathML/annotation on this site) is reverse-translated to
  LaTeX: text atoms, sup/sub via vlist entry offsets, frac, sqrt, big ops,
  delimiters, mtable environments (pmatrix/bmatrix/cases/aligned/matrix).
"""
import concurrent.futures as cf
import os
import re
import sys
import time
import urllib.request

from bs4 import BeautifulSoup, NavigableString, Tag
from markdownify import MarkdownConverter

BASE = 'https://www.csgraduates.com'
OUT_DIR = os.path.join(os.path.dirname(__file__), '..', 'data', 'exams', 'csgraduates')
CACHE_DIR = '/tmp/csgrad_cache'
UA = {'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36'}
WORKERS = 6

SYM = {
    '−': '-', '–': '--', '—': '---', '‘': "'", '’': "'", '“': '"', '”': '"',
    '×': r'\times', '÷': r'\div', '⋅': r'\cdot', '≤': r'\le', '≥': r'\ge',
    '≠': r'\ne', '≈': r'\approx', '≡': r'\equiv', '∈': r'\in', '∉': r'\notin',
    '⊂': r'\subset', '⊆': r'\subseteq', '∪': r'\cup', '∩': r'\cap',
    '∅': r'\varnothing', '∞': r'\infty', '∑': r'\sum', '∏': r'\prod',
    '∫': r'\int', '∬': r'\iint', '∭': r'\iiint', '∮': r'\oint',
    '±': r'\pm', '∓': r'\mp', '→': r'\to', '←': r'\leftarrow', '↔': r'\leftrightarrow',
    '⇒': r'\Rightarrow', '⇐': r'\Leftarrow', '⇔': r'\Leftrightarrow',
    '∀': r'\forall', '∃': r'\exists', '∂': r'\partial', '∇': r'\nabla',
    '⊥': r'\perp', '∥': r'\parallel', '∠': r'\angle', '°': r'^{\circ}',
    '⋯': r'\cdots', '…': r'\cdots', '⁄': '/', '√': r'\sqrt', '∝': r'\propto',
    '∽': r'\sim', '~': r'\sim', '≈': r'\approx', '∴': r'\therefore',
    '∵': r'\because', '′': "'", '″': "''",
    'Γ': r'\Gamma', 'Δ': r'\Delta', 'Θ': r'\Theta', 'Λ': r'\Lambda',
    'Ξ': r'\Xi', 'Π': r'\Pi', 'Σ': r'\Sigma', 'Φ': r'\Phi', 'Ψ': r'\Psi',
    'Ω': r'\Omega',
    'α': r'\alpha', 'β': r'\beta', 'γ': r'\gamma', 'δ': r'\delta',
    'ε': r'\varepsilon', 'ζ': r'\zeta', 'η': r'\eta', 'θ': r'\theta',
    'ι': r'\iota', 'κ': r'\kappa', 'λ': r'\lambda', 'μ': r'\mu',
    'ν': r'\nu', 'ξ': r'\xi', 'π': r'\pi', 'ρ': r'\rho', 'σ': r'\sigma',
    'τ': r'\tau', 'υ': r'\upsilon', 'φ': r'\varphi', 'χ': r'\chi',
    'ψ': r'\psi', 'ω': r'\omega', 'ϕ': r'\phi', 'ϱ': r'\varrho',
    'θ': r'\theta', 'ε': r'\epsilon',
}
OPS = {'∑': r'\sum', '∏': r'\prod', '∫': r'\int', '∬': r'\iint',
       '∭': r'\iiint', '∮': r'\oint', '⋃': r'\bigcup', '⋂': r'\bigcap',
       '⋀': r'\bigwedge', '⋁': r'\bigvee', '⊕': r'\bigoplus', '⊗': r'\bigotimes',
       'lim': r'\lim', 'max': r'\max', 'min': r'\min', 'sup': r'\sup', 'inf': r'\inf'}
FUNCS = {'sin', 'cos', 'tan', 'cot', 'sec', 'csc', 'arcsin', 'arccos', 'arctan',
         'sinh', 'cosh', 'tanh', 'ln', 'log', 'lim', 'max', 'min', 'exp', 'det',
         'dim', 'arg', 'Var', 'deg', 'gcd'}
LEAF_RE = re.compile(
    r'^/study_methods/(408quiz|math/math[123]|math_old|english/english[12]|politics)/([\w]+)(?:/([\w]+))?/?$')


def classify(path):
    m = LEAF_RE.match(path.rstrip('/') + '/')
    if not m:
        return None
    cat, a, b = m.groups()
    if cat == '408quiz':
        return f'408/{a}'
    if cat == 'politics':
        return f'politics/{a}'
    if cat == 'math_old':
        return f'math/math_old/{a}_{b}'
    if cat.startswith('math/'):
        return f'math/{cat.split("/")[1]}/{a}'
    if cat.startswith('english'):
        return f'english/{cat.split("/")[1]}/{a}'
    return None


def fetch(url, dest=None, retries=3):
    for i in range(retries):
        try:
            req = urllib.request.Request(url, headers=UA)
            with urllib.request.urlopen(req, timeout=40) as r:
                data = r.read()
            if dest:
                os.makedirs(os.path.dirname(dest), exist_ok=True)
                with open(dest, 'wb') as f:
                    f.write(data)
            return data
        except Exception as e:
            if i == retries - 1:
                raise
            time.sleep(1.5 * (i + 1))


# ---------------- KaTeX HTML -> LaTeX ----------------

def _top(el):
    m = re.search(r'top:\s*(-?[\d.]+)', el.get('style', ''))
    return float(m.group(1)) if m else None


def _entries(vlist):
    out = []
    for ch in vlist.find_all(recursive=False):
        if not isinstance(ch, Tag):
            continue
        cls = set(ch.get('class', []))
        if cls & {'vlist-s'} or 'frac-line' in cls:
            continue
        t = _top(ch)
        if t is not None:
            out.append((t, ch))
    return out


def _txt(el):
    return el.get_text()


def _map_char(s):
    out = []
    for c in s:
        v = SYM.get(c)
        if v is not None:
            # control words need a trailing space so they can't glue to the
            # next letter (\partial z); LaTeX ignores redundant spaces
            if re.fullmatch(r'\\[a-zA-Z]+', v):
                v += ' '
            out.append(v)
        else:
            out.append(c)
    return ''.join(out)


def _svg_delim_char(d):
    nums = re.findall(r'-?\d*\.?\d+', d)
    if len(nums) < 2:
        return ''
    head = ' '.join(nums[:2])
    n602 = sum(1 for n in nums if n == '602')
    if head == '863 9':
        return '('
    if head == '76 0':
        return ')'
    if head == '403 1759':
        return '[' if n602 == 0 else '⌈'
    if head == '347 1759':
        return ']' if n602 == 0 else '⌉'
    if head == '145 15':
        return '‖' if d.count('M') >= 3 else '|'
    return ''


def _delim_char(el):
    for c in el.get_text():
        if c in '⎧⎨⎩{':
            return '{'
        if c in '⎫⎬⎭}':
            return '}'
        if c.strip() and c != '.':
            return c
    svg = el.select_one('svg path')
    if svg is not None:
        return _svg_delim_char(svg.get('d', ''))
    return ''


_ENV_ESC = {'{': r'\{', '}': r'\}'}


def _env_name(l, r):
    if l == '(' or r == '(':
        return 'pmatrix'
    if l == '[' or r == '[':
        return 'bmatrix'
    if l == '{' and r == '}':
        return 'Bmatrix'
    if l == '{':
        return 'cases'
    if l == '|':
        return 'vmatrix'
    if l == '‖':
        return 'Vmatrix'
    return 'matrix'


def _find_env(el):
    """(left, right, mtable) if el wraps [delim][mtable][delim] (KaTeX renders
    matrix environments and cases exactly this way)."""
    delim_chars, mt = [], None
    for c in el.find_all(recursive=False):
        if not isinstance(c, Tag):
            continue
        d = c.find(class_='delimsizing')
        if d is not None:
            delim_chars.append(_delim_char(d))
        if mt is None and c.find(class_='mtable') is not None:
            mt = c.find(class_='mtable')
    if mt is not None and delim_chars:
        l = delim_chars[0]
        r = delim_chars[1] if len(delim_chars) > 1 else '.'
        return l, r, mt
    return None


def render(el):
    if isinstance(el, NavigableString):
        return _map_char(str(el))
    if not isinstance(el, Tag):
        return ''
    cls = set(el.get('class', []))
    if cls & {'strut', 'pstrut', 'mspace', 'vlist-s', 'vlist-r', 'nulldelimiter',
              'frac-line', 'svg-align', 'hide-tail', 'delim-size1', 'delim-size4',
              'delimsizinginner', 'vert-lines', 'hlineline'} and 'delimsizing' not in cls:
        return ''
    if 'katex-html' in cls or 'base' in cls:
        return _kids(el)
    env = _find_env(el)
    if env is not None:
        l, r, mt = env
        return _mtable(mt, _env_name(l, r))
    if cls & {'vlist-t', 'vlist'}:
        return _kids(el)
    if 'delimsizing' in cls:
        ch = _delim_char(el)
        return _ENV_ESC.get(ch, ch)
    if 'sqrt' in cls:
        body = el.select_one('.vlist-r > .vlist > span.svg-align') or el.select_one('.vlist-r > .vlist > span')
        inner = _kids(body) if body is not None else ''
        root = el.select_one('span.root')
        if root is not None and _txt(root).strip():
            return r'\sqrt[%s]{%s}' % (_kids(root).strip(), inner)
        return r'\sqrt{%s}' % inner
    if 'mfrac' in cls:
        vls = el.select('.vlist-r > .vlist')
        ents = _entries(vls[0]) if vls else []
        if len(ents) >= 2:
            ents.sort(key=lambda x: x[0])          # numerator = smallest top
            num = _kids(ents[0][1]).strip()
            den = _kids(ents[-1][1]).strip()
            return r'\frac{%s}{%s}' % (num, den)
        return _kids(el)
    if 'msupsub' in cls or cls & {'msup', 'msub', 'msubsup'}:
        return _kids(el)
    if 'op-limits' in cls:
        vl = el.select_one('.vlist-t > .vlist-r > .vlist')
        ents = _entries(vl) if vl else []
        core = [e for e in ents if not e[1].find(class_='sizing')]
        lims = sorted([e for e in ents if e[1].find(class_='sizing')], key=lambda x: -x[0])
        op = _map_char(_txt(core[0][1]).strip()) if core else ''
        op = OPS.get(op, op)
        below = _kids(lims[0][1]).strip() if lims else ''
        above = _kids(lims[-1][1]).strip() if len(lims) > 1 else ''
        if below and above:
            return r'%s_{%s}^{%s}' % (op, below, above)
        if below:
            return r'%s_{%s}' % (op, below)
        if above:
            return r'%s^{%s}' % (op, above)
        return op
    if 'delim-center' in cls:
        return _txt(el).strip()
    if 'mtable' in cls or any('col-align' in c for c in cls):
        return _mtable(el)
    if cls & {'msup', 'msub', 'msubsup'}:
        vl = el.select_one('.vlist-r > .vlist')
        ents = _entries(vl) if vl else []
        base = ''
        if 'msupsub' not in cls:
            base = _kids_nonsub(el)
        return base + _scripts(ents)
    if 'accent' in cls or 'accent-body' in cls:
        inner = el.select_one('.accent-body')
        return r'\bar{%s}' % _kids(inner) if inner is not None else _kids(el)
    if 'text' in cls and 'mtight' not in cls and 'context' not in cls:
        inner = _kids(el).strip()
        return r'\text{%s}' % inner if inner else ''
    if 'mop' in cls and el.find('span', class_='op-symbol', recursive=False) is None \
            and el.find('span', class_='msupsub', recursive=False) is None \
            and el.select_one('.vlist-t') is None:
        txt = _txt(el).strip()
        if txt in FUNCS or txt in OPS:
            return OPS.get(txt, '\\' + txt + ' ')
    # generic atom span (mord/mbin/mrel/mopen/mclose/mpunct/minner/mop/mathnormal...)
    scripts = el.find('span', class_='msupsub', recursive=False)
    if scripts is not None:
        vl = scripts.select_one('.vlist-r > .vlist')
        ents = _entries(vl) if vl else []
        body = []
        for ch in el.find_all(recursive=False):
            if ch is scripts:
                continue
            body.append(render(ch))
        return ''.join(body) + _scripts(ents)
    op = el.find('span', class_='op-symbol', recursive=False)
    if op is not None:
        return _map_char(_txt(op).strip())
    return _kids(el)


def _kids(el):
    return ''.join(render(c) for c in el.children if isinstance(c, (NavigableString, Tag)))


def _kids_pre(el, exclude=None):
    return ''.join(render(c) for c in el.children
                   if c is not exclude and isinstance(c, (NavigableString, Tag)))


def _scripts(ents):
    if not ents:
        return ''
    if len(ents) == 1:
        t, ch = ents[0]
        content = _kids(ch).strip()
        if not content:
            return ''
        return '^{%s}' % content if t < -2.6 else '_{%s}' % content
    ents.sort(key=lambda x: -x[0])  # larger top (lower) = subscript
    sub = _kids(ents[0][1]).strip()
    sup = _kids(ents[-1][1]).strip()
    if sub and sup:
        return '_{%s}^{%s}' % (sub, sup)
    if sub:
        return '_{%s}' % sub
    if sup:
        return '^{%s}' % sup
    return ''


def _mtable(mt, env=None):
    kids = [c for c in mt.find_all(recursive=False) if isinstance(c, Tag)]
    cols, is_col = [], []
    for c in kids:
        ccls = ''.join(c.get('class', []))
        if 'col-align' in ccls:
            vl = c.select_one('.vlist-r > .vlist')
            ents = _entries(vl) if vl else []
            cols.append({round(t, 1): _kids(ch).strip() for t, ch in ents})
            is_col.append(True)
        else:
            cols.append(None)
            is_col.append(False)
    # merge aligned pairs (col-align-r immediately followed by col-align-l)
    merged, i, had_merge = [], 0, False
    while i < len(kids):
        if is_col[i] and i + 1 < len(kids) and is_col[i + 1] \
                and 'col-align-r' in kids[i].get('class', []) \
                and 'col-align-l' in kids[i + 1].get('class', []):
            rowmap = {}
            for k in set(cols[i]) | set(cols[i + 1]):
                rowmap[k] = cols[i].get(k, '') + cols[i + 1].get(k, '')
            merged.append(rowmap)
            had_merge = True
            i += 2
        elif is_col[i]:
            merged.append(cols[i])
            i += 1
        else:
            i += 1
    if not merged:
        return ''
    if env is None:
        env = 'aligned' if had_merge else 'matrix'
    rows = {}
    for ci, col in enumerate(merged):
        for t, content in col.items():
            rows.setdefault(t, {})[ci] = content
    body = ' \\\\\n'.join(' & '.join(rows[t].get(ci, '') for ci in range(len(merged)))
                          for t in sorted(rows))
    return r'\begin{%s}%s\end{%s}' % (env, '\n' + body + '\n', env)


_CMDS = set()  # kept for reference; spacing now handled in _map_char


def katex_to_latex(kh):
    if kh is None:
        return ''
    return _kids(kh).replace('\u200b', '').strip()


# ---------------- Markdown conversion ----------------

class Conv(MarkdownConverter):
    def convert_br(self, el, text, parent_tags=None):
        return '\n'


def md_of(el):
    return Conv(heading_style='ATX', bullets='-').convert_soup(el)


def _fix_math_inner(inner):
    inner = inner.replace('\u200b', '')
    inner = inner.replace('\\\\', '\x00')
    inner = re.sub(r'\\([_*`\[\]<>#~])', r'\1', inner)   # undo markdown escapes
    return inner.replace('\x00', '\\\\')


def clean_spaces(md):
    md = re.sub(r'\$\$([\s\S]*?)\$\$', lambda m: '$$%s$$' % _fix_math_inner(m.group(1)), md)
    md = re.sub(r'\$([^$\n]+)\$', lambda m: '$%s$' % _fix_math_inner(m.group(1)), md)
    # pull inline math back onto the surrounding text lines (keep \n\n breaks
    # and never merge into list/table/heading lines)
    nomerge = r'(?![\n$])(?!\s*([-+*>]|\d+[.)]|\||#))'
    md = re.sub(r'(?<!\n)[ \t]*\n[ \t]*' + nomerge + r'(\$[^$\n]+\$)', r' \1', md)
    md = re.sub(r'(\$[^$\n]+\$)[ \t]*\n[ \t]*' + nomerge, r'\1 ', md)
    return md


def normalize_ws_around_math(node):
    """Collapse whitespace-only text nodes that sit next to inline math strings."""
    for s in list(node.find_all(string=True)):
        if not isinstance(s, NavigableString) or s.strip():
            continue
        prev, nxt = s.previous_sibling, s.next_sibling
        def has_math(x):
            return isinstance(x, NavigableString) and '$' in str(x)
        if has_math(prev) or has_math(nxt):
            sp = s.replace_with(' ')
            if '\n' in str(sp):
                pass  # replaced regardless


def process(html, rel_dir, page_url):
    soup = BeautifulSoup(html, 'html.parser')
    content = soup.select_one('div.td-content')
    title_el = content.select_one('h1')
    title = title_el.get_text(strip=True) if title_el else ''
    if title_el:
        title_el.decompose()

    # escape literal dollar signs (currency in passages) before math insertion
    for s in content.find_all(string=True):
        if isinstance(s, NavigableString) and '$' in s \
                and not s.find_parent(['pre', 'code']):
            s.replace_with(str(s).replace('$', r'\$'))

    # KaTeX -> inline/display math; empty formulas (cloze blanks) -> ______
    for k in content.select('span.katex'):
        disp = k.parent is not None and 'katex-display' in (k.parent.get('class') or [])
        latex = katex_to_latex(k.select_one('.katex-html'))
        if not latex:
            k.replace_with(NavigableString('______'))
            continue
        k.replace_with(NavigableString('$$%s$$' % latex if disp else '$%s$' % latex))
    for d in content.select('div.katex-display'):
        if not d.get_text(strip=True):
            d.decompose()
    normalize_ws_around_math(content)
    # protect underscore blank runs from markdown emphasis parsing
    blank_run = re.compile(r'_{3,}')
    for s in content.find_all(string=True):
        if isinstance(s, NavigableString) and '___' in str(s):
            s.replace_with(blank_run.sub('@@BLANK@@', str(s)))

    # choice questions
    for c in content.select('div.choice-container'):
        ans = c.get('data-answer', '')
        tags = c.get('data-tags', '')
        expl = c.select_one('.explanation')
        if expl:
            for st in expl.find_all('strong'):
                if '正确答案' in st.get_text():
                    nxt = st.next_sibling
                    if isinstance(nxt, NavigableString) and nxt.strip() in (':', '：'):
                        nxt.extract()
                    st.extract()
                    break
        opts = soup.new_tag('ul')
        for lbl in c.select('label.choice-option'):
            letter = lbl.select_one('.choice-label').get_text(strip=True)
            text = lbl.select_one('.choice-text')
            li = soup.new_tag('li')
            li.append(NavigableString(letter + ' '))
            if text:
                for ch in list(text.children):
                    li.append(ch.extract() if isinstance(ch, Tag) else ch)
            opts.append(li)
        piece = soup.new_tag('div')
        piece.append(opts)
        p = soup.new_tag('p')
        p.append(soup.new_tag('strong'))
        p.strong.string = '【答案】%s' % ans
        if tags:
            p.append(NavigableString('　【标签】%s' % tags))
        piece.append(p)
        if expl and expl.get_text(strip=True):
            ep = soup.new_tag('p')
            if not expl.get_text(strip=True).startswith('【解析】'):
                ep.append(soup.new_tag('strong'))
                ep.strong.string = '【解析】'
                expl.insert(0, ep)
            piece.append(expl)
        c.replace_with(piece)

    # essay questions: answer-container (tags) + following solution-detail
    for a in content.select('div.answer-container'):
        tags = a.get('data-tags', '')
        p = soup.new_tag('p')
        p.append(soup.new_tag('strong'))
        p.strong.string = '【标签】%s' % tags if tags else '【解答】'
        nxt = a.next_sibling
        while isinstance(nxt, NavigableString) and not nxt.strip():
            nxt = nxt.next_sibling
        a.replace_with(p)
        if isinstance(nxt, Tag) and 'solution-detail' in (nxt.get('class') or []) \
                and not nxt.get_text(strip=True).startswith('【解析】'):
            hp = soup.new_tag('p')
            hp.append(soup.new_tag('strong'))
            hp.strong.string = '【解析】'
            nxt.insert(0, hp)

    # answer quick-check <details> -> visible blockquote
    for d in content.select('details'):
        bq = soup.new_tag('blockquote')
        sm = d.select_one('summary')
        if sm:
            bp = soup.new_tag('p')
            bp.append(soup.new_tag('strong'))
            bp.strong.string = sm.get_text(strip=True)
            bq.append(bp)
            sm.decompose()
        for ch in list(d.children):
            bq.append(ch.extract() if isinstance(ch, Tag) else ch)
        d.replace_with(bq)

    for sel in ('.video-links', '.quiz-tag-container', '.quiz-actions', '.feedback-area',
                'header.article-meta', 'script', 'style'):
        for el in content.select(sel):
            el.decompose()

    # images
    assets_rel = None
    for im in content.select('img'):
        src = im.get('src', '')
        if not src or src.startswith('data:'):
            continue
        full = src if src.startswith('http') else BASE + src
        site_rel = src.lstrip('/') if not src.startswith('http') else os.path.basename(src)
        local = os.path.join(OUT_DIR, 'assets', site_rel)
        if not os.path.exists(local):
            try:
                fetch(full, dest=local)
            except Exception as e:
                print('  img FAIL %s: %s' % (full, e))
                im.decompose()
                continue
        rel = os.path.relpath(local, os.path.join(OUT_DIR, rel_dir))
        im['src'] = rel

    md = md_of(content)
    md = clean_spaces(md)
    md = md.replace('@@BLANK@@', '______')
    md = re.sub(r'\n{3,}', '\n\n', md).strip() + '\n'
    return title, md


def page_info(url):
    path = re.sub(r'^https?://[^/]+', '', url)
    rel = classify(path)
    return rel


def main():
    os.makedirs(CACHE_DIR, exist_ok=True)
    os.makedirs(OUT_DIR, exist_ok=True)
    leaves_f = '/tmp/leaves.txt'
    if os.path.exists(leaves_f):
        urls = [l.strip().replace('http://', 'https://') for l in open(leaves_f) if l.strip()]
    else:
        sm = fetch(BASE + '/sitemap.xml').decode()
        urls = sorted(set(re.findall(r'<loc>([^<]+)</loc>', sm)))
        urls = [u for u in urls if page_info(u.replace('http://', 'https://'))]
    urls = [u for u in urls if page_info(u)]
    print('leaf pages:', len(urls))

    def work(url):
        rel = page_info(url)
        cache = os.path.join(CACHE_DIR, rel + '.html')
        if os.path.exists(cache) and os.path.getsize(cache) > 10000:
            html = open(cache, 'rb').read()
        else:
            html = fetch(url, dest=cache)
        out_md = os.path.join(OUT_DIR, rel + '.md')
        if os.path.exists(out_md) and os.path.getsize(out_md) > 500:
            return (url, 'skip')
        try:
            title, md = process(html, rel, url)
        except Exception as e:
            return (url, 'FAIL %r' % e)
        os.makedirs(os.path.dirname(out_md), exist_ok=True)
        with open(out_md, 'w') as f:
            f.write('---\ntitle: "%s"\nsource: %s\n---\n\n' % (title, url))
            f.write(md)
        return (url, 'ok')

    results = []
    with cf.ThreadPoolExecutor(WORKERS) as ex:
        for i, res in enumerate(ex.map(work, urls)):
            results.append(res)
            if i % 20 == 0:
                print('  %d/%d' % (i + 1, len(urls)))
    bad = [r for r in results if r[1] not in ('ok', 'skip')]
    print('done. ok=%d skip=%d fail=%d' % (
        sum(1 for r in results if r[1] == 'ok'),
        sum(1 for r in results if r[1] == 'skip'), len(bad)))
    for r in bad[:20]:
        print(' ', r)


if __name__ == '__main__':
    main()
