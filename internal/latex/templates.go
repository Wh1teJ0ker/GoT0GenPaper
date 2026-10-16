// Package latex provides LaTeX document generation for printable exam papers.
package latex

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	texttemplate "text/template"
	"unicode/utf8"
)

var (
	templateMu           sync.RWMutex
	templateDir          = defaultTemplateDir()
	latexTemplates       *texttemplate.Template
	verbatimBlockPattern = regexp.MustCompile(`(?s)\\begin\{verbatim\}.*?\\end\{verbatim\}`)
)

// requiredTemplateNames is the contract of the external template directory.
// A malformed configuration should fail at startup, not during rendering.
var requiredTemplateNames = []string{
	"cloze_question",
	"choice_options",
	"choice_question",
	"document_preamble",
	"exam_document",
	"major_question",
	"question_choice",
	"question_fill_blank",
	"question_major",
	"question_multi_choice",
}

func defaultTemplateDir() string {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "templates"
	}
	return filepath.Join(filepath.Dir(source), "..", "..", "templates")
}

func parseTemplates(dir string) (*texttemplate.Template, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.tmpl"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .tmpl files found in %s", dir)
	}
	templates, err := texttemplate.New("latex").Delims("{{%", "%}}").ParseFiles(files...)
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0)
	for _, name := range requiredTemplateNames {
		if templates.Lookup(name) == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required templates: %s", strings.Join(missing, ", "))
	}
	return templates, nil
}

// SetTemplateDir loads the external LaTeX templates selected by configuration.
func SetTemplateDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("LaTeX template directory is empty")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	templates, err := parseTemplates(absolute)
	if err != nil {
		return fmt.Errorf("load LaTeX templates: %w", err)
	}
	templateMu.Lock()
	templateDir = absolute
	latexTemplates = templates
	templateMu.Unlock()
	return nil
}

func currentTemplates() *texttemplate.Template {
	templateMu.RLock()
	templates := latexTemplates
	dir := templateDir
	templateMu.RUnlock()
	if templates != nil {
		return templates
	}
	parsed, err := parseTemplates(dir)
	if err != nil {
		panic(fmt.Errorf("load LaTeX templates: %w", err))
	}
	templateMu.Lock()
	if latexTemplates == nil {
		latexTemplates = parsed
	}
	templates = latexTemplates
	templateMu.Unlock()
	return templates
}

func renderTemplate(name string, data any) string {
	var out strings.Builder
	if err := currentTemplates().ExecuteTemplate(&out, name, data); err != nil {
		panic(fmt.Errorf("render LaTeX template %q: %w", name, err))
	}
	return out.String()
}

// QuestionSkeleton returns the LLM-facing LaTeX scaffold for a question type.
func QuestionSkeleton(questionType string) string {
	name := "question_" + questionType
	templates := currentTemplates()
	if templates.Lookup(name) == nil {
		name = "question_major"
	}
	return renderTemplate(name, nil)
}

// FooterBranding is the three-part page footer of the exam paper.
type FooterBranding struct {
	Left   string
	Center string
	Right  string
}

// ExamMeta describes one exam paper's identity.
type ExamMeta struct {
	Year      string
	Subject   string
	FullTitle string
}

// escapeLaTeX escapes text outside math segments while retaining math markup.
func escapeLaTeX(s string) string {
	var verbatimBlocks []string
	s, verbatimBlocks = protectVerbatimBlocks(s)
	s = normalizeTeXControlCharacters(s)
	s = normalizePreEscapedSpecialCharacters(s)
	s = normalizeCasesNewlineEscapes(s)
	s = normalizeDisplayMathWhitespace(s)
	s = normalizeFillBlankMarkers(s)
	s = normalizeLongTextUnderlines(s)
	s = normalizeGeneratedTables(s)
	s = normalizeTextWrappers(s)
	s = normalizeUnicodeLookalikes(s)
	var b strings.Builder
	mathDelimiter := ""
	runes := []rune(s)
	if strings.Count(s, "$")%2 == 1 {
		for _, r := range runes {
			switch r {
			case '$':
				b.WriteString(`\$`)
			case '&':
				b.WriteString(`\&`)
			case '%':
				b.WriteString(`\%`)
			case '#':
				b.WriteString(`\#`)
			case '_':
				b.WriteString(`\_`)
			case '^':
				b.WriteString(`\textasciicircum{}`)
			case '~':
				b.WriteString(`\textasciitilde{}`)
			default:
				b.WriteRune(r)
			}
		}
		return restoreVerbatimBlocks(b.String(), verbatimBlocks)
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '√' || r == '∛' {
			command := `\sqrt`
			if r == '∛' {
				command = `\sqrt[3]`
			}
			operand := ""
			end := i
			if i+1 < len(runes) && runes[i+1] == '(' {
				close := i + 2
				for close < len(runes) && runes[close] != ')' {
					close++
				}
				if close < len(runes) {
					operand = string(runes[i+2 : close])
					end = close
				}
			}
			if operand == "" && i+1 < len(runes) {
				operand = string(runes[i+1])
				end = i + 1
			}
			if operand != "" {
				if mathDelimiter == "" {
					b.WriteString(`\ensuremath{`)
				}
				b.WriteString(command)
				b.WriteByte('{')
				b.WriteString(operand)
				b.WriteByte('}')
				if mathDelimiter == "" {
					b.WriteByte('}')
				}
				i = end
				continue
			}
		}
		if r == '\\' && i+1 < len(runes) && strings.ContainsRune("[]()", runes[i+1]) {
			delimiter := string([]rune{r, runes[i+1]})
			if delimiter == `\[` || delimiter == `\(` {
				mathDelimiter = delimiter
			} else if (delimiter == `\]` && mathDelimiter == `\[`) || (delimiter == `\)` && mathDelimiter == `\(`) {
				mathDelimiter = ""
			}
			b.WriteString(delimiter)
			i++
			continue
		}
		if r == '\\' && i+1 < len(runes) && strings.ContainsRune(`%&#_^~$`, runes[i+1]) {
			// Preserve a special character that was already escaped by the
			// source or the model. Escaping its second rune again would emit
			// invalid TeX such as `ack\\\\_seq`.
			special := runes[i+1]
			switch special {
			case '^':
				if mathDelimiter == "" {
					b.WriteString(`\textasciicircum{}`)
				} else {
					b.WriteRune('^')
				}
			case '~':
				if mathDelimiter == "" {
					b.WriteString(`\textasciitilde{}`)
				} else {
					b.WriteString(`\sim`)
				}
			default:
				b.WriteByte('\\')
				b.WriteRune(special)
			}
			i++
			continue
		}
		if r == '$' {
			if i+1 < len(runes) && runes[i+1] == '$' {
				if mathDelimiter == "" {
					mathDelimiter = "$$"
				} else if mathDelimiter == "$$" {
					mathDelimiter = ""
				}
				b.WriteString("$$")
				i++
			} else {
				if mathDelimiter == "" {
					mathDelimiter = "$"
				} else if mathDelimiter == "$" {
					mathDelimiter = ""
				}
				b.WriteRune(r)
			}
			continue
		}
		if mathDelimiter != "" {
			switch {
			case r == '%':
				b.WriteString(`\%`)
			case unicodeSuperscript(r) != 0:
				b.WriteByte('^')
				b.WriteRune(unicodeSuperscript(r))
			case unicodeSubscript(r) != 0:
				b.WriteByte('_')
				b.WriteRune(unicodeSubscript(r))
			case unicodeMathCommand(r) != "":
				b.WriteString(unicodeMathCommand(r))
			default:
				b.WriteRune(r)
			}
			continue
		}
		if command := unicodeMathCommand(r); command != "" {
			b.WriteString(`\ensuremath{` + command + `}`)
			continue
		}
		if digit := unicodeSuperscript(r); digit != 0 {
			b.WriteString(`\textsuperscript{`)
			b.WriteRune(digit)
			b.WriteByte('}')
			continue
		}
		if digit := unicodeSubscript(r); digit != 0 {
			b.WriteString(`\textsubscript{`)
			b.WriteRune(digit)
			b.WriteByte('}')
			continue
		}
		switch r {
		case '&':
			b.WriteString(`\&`)
		case '%':
			b.WriteString(`\%`)
		case '#':
			b.WriteString(`\#`)
		case '_':
			b.WriteString(`\_`)
		case '^':
			b.WriteString(`\textasciicircum{}`)
		case '~':
			b.WriteString(`\textasciitilde{}`)
		default:
			b.WriteRune(r)
		}
	}
	return restoreVerbatimBlocks(b.String(), verbatimBlocks)
}

func protectVerbatimBlocks(s string) (string, []string) {
	var blocks []string
	protected := verbatimBlockPattern.ReplaceAllStringFunc(s, func(block string) string {
		blocks = append(blocks, block)
		return fmt.Sprintf("GOT0GENPAPERVERBATIMBLOCK%04dEND", len(blocks)-1)
	})
	return protected, blocks
}

func restoreVerbatimBlocks(s string, blocks []string) string {
	for i, block := range blocks {
		marker := fmt.Sprintf("GOT0GENPAPERVERBATIMBLOCK%04dEND", i)
		s = strings.ReplaceAll(s, marker, block)
	}
	return s
}

// normalizeLongTextUnderlines prevents generated translation passages from
// becoming one unbreakable TeX box. LaTeX's underline command cannot wrap a
// long sentence across lines; bold text keeps the target sentence visible and
// allows normal line breaking. Short answer blanks remain underlined.
func normalizeLongTextUnderlines(s string) string {
	const marker = `\underline{`
	offset := 0
	for {
		relative := strings.Index(s[offset:], marker)
		if relative < 0 {
			return s
		}
		start := offset + relative
		open := start + len(`\underline`)
		end := balancedBraceEnd(s, open)
		if end < 0 {
			return s
		}
		content := s[open+1 : end]
		if utf8.RuneCountInString(content) <= 80 {
			offset = end + 1
			continue
		}
		replacement := `\textbf{` + content + `}`
		s = s[:start] + replacement + s[end+1:]
		offset = start + len(replacement)
	}
}

// normalizeGeneratedTables converts raw table environments from model output
// into ordinary, line-breakable text. Generated table syntax is often
// incomplete (escaped alignment markers, row breaks before percent signs, or
// a float at the end of the document); preserving it verbatim can make XeTeX
// fail with an unrelated prevdepth error. Cell contents remain visible.
// Bare tabular blocks — not wrapped in a table float, as produced by English
// chart-prompt questions — are flattened the same way; left intact, the
// generic escaper would escape their & separators and collapse the chart into
// one literal "&"-riddled line.
func normalizeGeneratedTables(s string) string {
	const begin = `\begin{table}`
	const endMarker = `\end{table}`
	for {
		start := strings.Index(s, begin)
		if start < 0 {
			return normalizeBareTabular(s)
		}
		relativeEnd := strings.Index(s[start:], endMarker)
		if relativeEnd < 0 {
			return normalizeBareTabular(s)
		}
		end := start + relativeEnd + len(endMarker)
		block := s[start:end]
		block = regexp.MustCompile(`\\begin\{table\}(?:\[[^]]*\])?|\\end\{table\}`).ReplaceAllString(block, "")
		block = regexp.MustCompile(`\\begin\{tabular\}\{[^{}]*\}|\\end\{tabular\}`).ReplaceAllString(block, "")
		block = strings.ReplaceAll(block, `\centering`, "")
		block = regexp.MustCompile(`\\caption\{[^{}]*\}`).ReplaceAllString(block, "")
		block = strings.ReplaceAll(block, `\hline`, "")
		block = strings.ReplaceAll(block, `\&`, " / ")
		block = strings.ReplaceAll(block, "&", " / ")
		block = strings.ReplaceAll(block, `\\`, "; ")
		// A source row may contain a single escaped percent after the row
		// separator has been flattened. Remove that source escape here so the
		// normal LaTeX escaper adds exactly one, not two.
		block = regexp.MustCompile(`\\+%`).ReplaceAllString(block, "%")
		block = regexp.MustCompile(`\\+\s*$`).ReplaceAllString(block, "")
		block = strings.Join(strings.Fields(block), " ")
		if block == "" {
			block = "（图表数据见题面）"
		} else {
			block = "Chart data: " + block
		}
		s = s[:start] + block + s[end:]
	}
}

// normalizeBareTabular flattens tabular environments that a model emitted
// without a surrounding table float. It applies the same cell-preserving
// flattening as the table path; a bare block that merely wraps a math array
// (the determinants and matrices math answers legitimately produce) is left
// untouched because arrays carry row semantics the text cannot.
func normalizeBareTabular(s string) string {
	const begin = `\begin{tabular}`
	const endMarker = `\end{tabular}`
	for {
		start := strings.Index(s, begin)
		if start < 0 {
			return s
		}
		relativeEnd := strings.Index(s[start:], endMarker)
		if relativeEnd < 0 {
			return s
		}
		end := start + relativeEnd + len(endMarker)
		// Extend the region over an optional \begin{center} wrapper so the
		// flattened chart does not strand a center environment around it.
		if centerBegin := `\begin{center}`; strings.HasSuffix(s[:start], centerBegin) {
			start -= len(centerBegin)
		}
		if centerEnd := `\end{center}`; strings.HasPrefix(s[end:], centerEnd) {
			end += len(centerEnd)
		}
		block := s[start:end]
		block = regexp.MustCompile(`\\begin\{tabular\}\{[^{}]*\}|\\end\{tabular\}`).ReplaceAllString(block, "")
		block = strings.ReplaceAll(block, `\centering`, "")
		block = strings.ReplaceAll(block, `\begin{center}`, "")
		block = strings.ReplaceAll(block, `\end{center}`, "")
		block = strings.ReplaceAll(block, `\hline`, "")
		block = strings.ReplaceAll(block, `\&`, " / ")
		block = strings.ReplaceAll(block, "&", " / ")
		block = strings.ReplaceAll(block, `\\`, "; ")
		block = regexp.MustCompile(`\\+%`).ReplaceAllString(block, "%")
		block = regexp.MustCompile(`\\+\s*$`).ReplaceAllString(block, "")
		block = strings.Join(strings.Fields(block), " ")
		if block == "" {
			block = "（图表数据见题面）"
		} else {
			block = "Chart data: " + block
		}
		s = s[:start] + block + s[end:]
	}
}

// normalizeCasesNewlineEscapes repairs a common flattened-JSON form of a
// cases environment: "\\begin{cases}\\nk_1 ...". Only known row starters and
// row separators are changed, so genuine commands such as \\neq remain intact.
func normalizeCasesNewlineEscapes(s string) string {
	const begin = `\begin{cases}`
	const end = `\end{cases}`
	for {
		start := strings.Index(s, begin)
		if start < 0 {
			return s
		}
		endOffset := strings.Index(s[start+len(begin):], end)
		if endOffset < 0 {
			return s
		}
		end := start + len(begin) + endOffset + len(end)
		region := s[start:end]
		for _, row := range []string{"k", "x", "y", "z"} {
			marker := `\n` + row
			region = strings.ReplaceAll(region, marker, "\n"+row)
		}
		for _, command := range []string{`\vdots`, `\cdots`} {
			marker := `\n` + command
			region = strings.ReplaceAll(region, marker, "\n"+command)
		}
		s = s[:start] + region + s[end:]
		return s
	}
}

// normalizeTeXControlCharacters repairs control bytes that can appear when a
// JSON answer contains a LaTeX command such as "\\frac" or "\\partial" but
// was decoded by a producer that interpreted the command's escape prefix.
// Newlines and tabs are preserved or normalized; other control bytes are
// invalid in a generated .tex source and are removed.
func normalizeTeXControlCharacters(s string) string {
	s = strings.ReplaceAll(s, "\f"+"rac", `\frac`)
	s = strings.ReplaceAll(s, "\\\x00partial", `\partial`)
	s = strings.ReplaceAll(s, "\x00partial", `\partial`)

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteRune(r)
		case '\r':
			b.WriteByte('\n')
		case '\t':
			b.WriteByte(' ')
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizePreEscapedSpecialCharacters collapses duplicate escape prefixes
// before TeX special characters. This handles flattened or hand-authored
// source such as `ack\\_seq` while leaving ordinary commands such as
// `\\frac` and line-break commands untouched.
func normalizePreEscapedSpecialCharacters(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' {
			start := i
			for i < len(s) && s[i] == '\\' {
				i++
			}
			if i < len(s) && strings.ContainsRune(`%&#_^~$`, rune(s[i])) {
				b.WriteByte('\\')
				b.WriteByte(s[i])
				i++
				continue
			}
			b.WriteString(s[start:i])
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func normalizeDisplayMathWhitespace(s string) string {
	// A blank line immediately after/before a display delimiter can terminate
	// the paragraph before TeX sees the matching delimiter, especially around
	// generated cases/aligned environments.
	s = strings.ReplaceAll(s, "$$\n\n", "$$\n")
	s = strings.ReplaceAll(s, "\n\n$$", "\n$$")
	s = strings.ReplaceAll(s, "\\[\n\n", "\\[\n")
	s = strings.ReplaceAll(s, "\n\n\\]", "\n\\]")
	return s
}

// normalizeTextWrappers unwraps \text{...} used by some English generations
// around ordinary prose. Outside math, the wrapper prevents normal line
// breaking and can create enormous overfull boxes; inside math it remains
// meaningful and is preserved.
func normalizeTextWrappers(s string) string {
	var out strings.Builder
	mathDelimiter := ""
	for i := 0; i < len(s); i++ {
		if mathDelimiter == "" {
			unwrapped := false
			for _, command := range []string{`\text{`, `\textit{`, `\emph{`} {
				if !strings.HasPrefix(s[i:], command) {
					continue
				}
				open := i + len(command) - 1
				if end := balancedBraceEnd(s, open); end >= 0 {
					out.WriteString(s[open+1 : end])
					i = end
					unwrapped = true
				}
				break
			}
			if unwrapped {
				continue
			}
		}
		if s[i] == '\\' && i+1 < len(s) && strings.ContainsRune("[]()", rune(s[i+1])) {
			delimiter := s[i : i+2]
			if delimiter == `\[` || delimiter == `\(` {
				mathDelimiter = delimiter
			} else if (delimiter == `\]` && mathDelimiter == `\[`) || (delimiter == `\)` && mathDelimiter == `\(`) {
				mathDelimiter = ""
			}
			out.WriteString(delimiter)
			i++
			continue
		}
		if s[i] == '$' {
			if i+1 < len(s) && s[i+1] == '$' {
				if mathDelimiter == "" {
					mathDelimiter = "$$"
				} else if mathDelimiter == "$$" {
					mathDelimiter = ""
				}
				out.WriteString("$$")
				i++
				continue
			}
			if mathDelimiter == "" {
				mathDelimiter = "$"
			} else if mathDelimiter == "$" {
				mathDelimiter = ""
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func balancedBraceEnd(s string, open int) int {
	if open < 0 || open >= len(s) || s[open] != '{' {
		return -1
	}
	depth := 0
	escaped := false
	for i := open; i < len(s); i++ {
		if escaped {
			escaped = false
			continue
		}
		if s[i] == '\\' {
			escaped = true
			continue
		}
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

var (
	escapedBlankMarker = regexp.MustCompile(`(?:\\_){2,}`)
	plainBlankMarker   = regexp.MustCompile(`_{3,}`)
)

// normalizeFillBlankMarkers turns common Markdown/LLM blank placeholders into
// the same printable blank used by the fill-in-the-blank template. Escaped
// underscores otherwise become a literal backslash followed by an escaped
// underscore after escapeLaTeX processes the surrounding prose.
func normalizeFillBlankMarkers(s string) string {
	const blank = `\underline{\quad\quad}`
	s = escapedBlankMarker.ReplaceAllString(s, blank)
	return plainBlankMarker.ReplaceAllString(s, blank)
}

// normalizeUnicodeLookalikes fixes common OCR/LLM substitutions of Latin
// variables with visually similar Cyrillic letters. Those runes are not
// available in TeX math fonts and otherwise cause a successful-looking
// document to fail with missing-glyph or math-mode errors.
func normalizeUnicodeLookalikes(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 'а':
			return 'a'
		case 'А':
			return 'A'
		case 'е':
			return 'e'
		case 'Е':
			return 'E'
		case 'о':
			return 'o'
		case 'О':
			return 'O'
		case 'р':
			return 'p'
		case 'Р':
			return 'P'
		case 'с':
			return 'c'
		case 'С':
			return 'C'
		case 'х':
			return 'x'
		case 'Х':
			return 'X'
		case 'у':
			return 'y'
		case 'У':
			return 'Y'
		default:
			return r
		}
	}, s)
}

func unicodeMathCommand(r rune) string {
	return map[rune]string{
		'∈': `\in`, '∉': `\notin`, '∀': `\forall`, '∃': `\exists`,
		'∂': `\partial`, '∫': `\int`, '∑': `\sum`, '∏': `\prod`,
		'∞': `\infty`, '≤': `\leq`, '≥': `\geq`, '≠': `\neq`, '±': `\pm`,
		'∪': `\cup`, '∩': `\cap`, 'α': `\alpha`, 'β': `\beta`, 'γ': `\gamma`,
		'δ': `\delta`, 'ε': `\epsilon`, 'θ': `\theta`, 'λ': `\lambda`,
		'μ': `\mu`, 'π': `\pi`, 'σ': `\sigma`, 'φ': `\phi`, 'ω': `\omega`,
		'Ω': `\Omega`, '×': `\times`, '·': `\cdot`, '→': `\to`, '←': `\leftarrow`,
		'↔': `\leftrightarrow`,
	}[r]
}

func unicodeSuperscript(r rune) rune {
	return map[rune]rune{
		'⁰': '0', '¹': '1', '²': '2', '³': '3', '⁴': '4',
		'⁵': '5', '⁶': '6', '⁷': '7', '⁸': '8', '⁹': '9',
		'⁻': '-', '⁺': '+',
	}[r]
}

func unicodeSubscript(r rune) rune {
	return map[rune]rune{'₀': '0', '₁': '1', '₂': '2', '₃': '3', '₄': '4', '₅': '5', '₆': '6', '₇': '7', '₈': '8', '₉': '9'}[r]
}

type optionLayout int

const (
	optionsInline4 optionLayout = iota
	optionsInline2
	optionsStacked
)

func decideOptionLayout(options []string) optionLayout {
	maxLen := 0
	for _, option := range options {
		if n := utf8.RuneCountInString(strings.TrimSpace(option)); n > maxLen {
			maxLen = n
		}
	}
	switch {
	case maxLen <= 6:
		return optionsInline4
	case maxLen <= 14:
		return optionsInline2
	default:
		return optionsStacked
	}
}

var optionLabelPrefix = regexp.MustCompile(`^[A-Ha-h][\.、．)）]\s*`)

type renderedOption struct {
	Label string
	Text  string
}

type renderedOptionRow struct {
	Options    []renderedOption
	Separator  string
	BlankAfter bool
}

func optionRows(options []string) []renderedOptionRow {
	const labels = "ABCDEFGH"
	layout := decideOptionLayout(options)
	perRow := len(options)
	switch layout {
	case optionsInline2:
		perRow = 2
	case optionsStacked:
		perRow = 1
	}
	if perRow == 0 {
		return nil
	}
	rows := make([]renderedOptionRow, 0, (len(options)+perRow-1)/perRow)
	for start := 0; start < len(options); start += perRow {
		end := start + perRow
		if end > len(options) {
			end = len(options)
		}
		separator := ""
		switch layout {
		case optionsInline4:
			separator = `\quad `
		case optionsInline2:
			separator = `\quad `
		}
		row := renderedOptionRow{Separator: separator, BlankAfter: layout != optionsInline4 && end < len(options)}
		for i, option := range options[start:end] {
			label := "?"
			if start+i < len(labels) {
				label = string(labels[start+i])
			}
			row.Options = append(row.Options, renderedOption{
				Label: label,
				Text:  escapeLaTeX(optionLabelPrefix.ReplaceAllString(strings.TrimSpace(option), "")),
			})
		}
		rows = append(rows, row)
	}
	return rows
}

func renderOptions(options []string) string {
	return renderTemplate("choice_options", optionRows(options))
}

// CleanMarkdownStem removes Markdown wrappers and safely folds flattened cells.
func CleanMarkdownStem(s string) string {
	var verbatimBlocks []string
	s, verbatimBlocks = protectVerbatimBlocks(s)
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "```") {
			continue
		}
		kept = append(kept, line)
	}
	out := make([]string, 0, len(kept))
	var fragments []string
	previousFragment := false
	inMath := false
	inVerbatim := false
	flush := func() {
		if len(fragments) > 0 {
			out = append(out, strings.Join(fragments, "  "))
			fragments = fragments[:0]
		}
	}
	for _, line := range kept {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, `\begin{verbatim}`) {
			flush()
			out = append(out, line)
			previousFragment = false
			inVerbatim = !strings.Contains(trimmed, `\end{verbatim}`)
			continue
		}
		if inVerbatim {
			out = append(out, line)
			if strings.Contains(trimmed, `\end{verbatim}`) {
				inVerbatim = false
			}
			continue
		}
		if strings.Contains(trimmed, `\[`) {
			inMath = true
		}
		if trimmed == "" {
			continue
		}
		if !inMath && isFragmentLine(trimmed) {
			fragments = append(fragments, trimmed)
			previousFragment = true
			continue
		}
		if previousFragment && !inMath {
			flush()
			out = append(out, "")
			previousFragment = false
		} else {
			flush()
		}
		out = append(out, line)
		if strings.Contains(trimmed, `\]`) {
			inMath = false
		}
	}
	flush()
	result := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(strings.Join(out, "\n"), "`", ""), "**", ""))
	var normalized strings.Builder
	normalized.Grow(len(result))
	for i := 0; i < len(result); i++ {
		if result[i] == '\\' && i+1 < len(result) && result[i+1] == 'n' &&
			!isKnownNCommand(result, i+1) {
			normalized.WriteByte('\n')
			i++
			continue
		}
		normalized.WriteByte(result[i])
	}
	return restoreVerbatimBlocks(strings.TrimSpace(normalized.String()), verbatimBlocks)
}

// isKnownNCommand distinguishes a literal JSON-style "\\nWord" newline from
// a real TeX command whose name starts with n (for example \\neq or \\nu).
// Generated prose commonly starts the next line with an ASCII word, so a
// generic "next byte is a letter" check is not sufficient.
func isKnownNCommand(s string, nIndex int) bool {
	if nIndex < 0 || nIndex >= len(s) || s[nIndex] != 'n' {
		return false
	}
	end := nIndex + 1
	for end < len(s) && isASCIILetter(s[end]) {
		end++
	}
	command := s[nIndex:end]
	switch command {
	case "neq", "nu", "noindent", "not", "notin", "nabla", "neg", "newline", "nobreak", "nobreakspace", "normalfont", "normalsize", "nocite", "newcommand", "newenvironment", "newunicodechar", "number", "num":
		return true
	default:
		return false
	}
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isFragmentLine(text string) bool {
	if text == "" || text[0] == '\\' {
		return false
	}
	runes := []rune(text)
	if len(runes) > 12 {
		return false
	}
	switch runes[len(runes)-1] {
	case '。', '！', '？', '；', '：', '.', ',', ';', ':', '!', '?', '，', '、':
		return false
	}
	return runes[0] != '(' && runes[0] != '（'
}

type SectionSpec struct {
	Title  string
	Detail string
}

type ExamSection struct {
	Spec      SectionSpec
	Questions []string
}

type footerTemplateData struct {
	Left  string
	Right string
}

type questionTemplateData struct {
	Number     int
	Score      string
	Stem       string
	OptionRows []renderedOptionRow
}

type clozeQuestionTemplateData struct {
	Number  int
	Options []renderedOption
}

type examSectionTemplateData struct {
	Title     string
	Detail    string
	Questions []string
}

type examDocumentTemplateData struct {
	Footer     footerTemplateData
	HasCover   bool
	CoverLines []string
	HeadLine   string
	FullTitle  string
	Subject    string
	Sections   []examSectionTemplateData
}

func ExamPaper(meta ExamMeta, sections []ExamSection, cover *[]string, footer FooterBranding) string {
	data := examDocumentTemplateData{
		Footer:    footerTemplateData{Left: escapeLaTeX(footer.Left), Right: escapeLaTeX(footer.Right)},
		HeadLine:  "全国硕士研究生入学统一考试",
		FullTitle: escapeLaTeX(meta.FullTitle),
		Subject:   escapeLaTeX(meta.Subject),
		Sections:  make([]examSectionTemplateData, 0, len(sections)),
	}
	if meta.Year != "" {
		data.HeadLine = meta.Year + " 年" + data.HeadLine
	}
	data.HeadLine = escapeLaTeX(data.HeadLine)
	if cover != nil {
		data.HasCover = true
		data.CoverLines = make([]string, len(*cover))
		for i, line := range *cover {
			data.CoverLines[i] = escapeLaTeX(line)
		}
	}
	for _, section := range sections {
		data.Sections = append(data.Sections, examSectionTemplateData{
			Title: escapeLaTeX(section.Spec.Title), Detail: escapeLaTeX(section.Spec.Detail),
			Questions: append([]string(nil), section.Questions...),
		})
	}
	return renderTemplate("exam_document", data)
}

func ChoiceQuestion(number int, stem string, options []string) string {
	return renderTemplate("choice_question", questionTemplateData{
		Number: number, Stem: escapeLaTeX(stem), OptionRows: optionRows(options),
	})
}

// ClozeQuestion renders the compact four-choice row used by English cloze
// questions. The question number is rendered once; the source stem remains in
// the shared passage and is therefore intentionally omitted here.
func ClozeQuestion(number int, options []string) string {
	const labels = "ABCD"
	rendered := make([]renderedOption, 0, 4)
	for i := 0; i < 4; i++ {
		option := ""
		if i < len(options) {
			option = options[i]
		}
		label := string(labels[i])
		rendered = append(rendered, renderedOption{
			Label: label,
			Text:  escapeLaTeX(optionLabelPrefix.ReplaceAllString(strings.TrimSpace(option), "")),
		})
	}
	return renderTemplate("cloze_question", clozeQuestionTemplateData{
		Number: number, Options: rendered,
	})
}

// ExamText renders unnumbered shared material such as a grouped reading
// passage, retaining the normal LaTeX escaping rules.
func ExamText(text string) string {
	return escapeLaTeX(text)
}

// ExamOptions renders an unnumbered shared choice list such as English II
// Part B's seven matching statements.
func ExamOptions(options []string) string {
	return renderTemplate("choice_options", optionRows(options))
}

func MajorQuestion(number int, score float64, stem string) string {
	return renderTemplate("major_question", questionTemplateData{
		Number: number, Score: strconv.FormatFloat(score, 'f', 0, 64), Stem: escapeLaTeX(stem),
	})
}

func examPreamble(footer FooterBranding, totalPages int) string {
	_ = totalPages
	return renderTemplate("document_preamble", footerTemplateData{
		Left: escapeLaTeX(footer.Left), Right: escapeLaTeX(footer.Right),
	})
}
