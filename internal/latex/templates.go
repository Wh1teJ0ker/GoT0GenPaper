// Package latex provides LaTeX document generation for exam papers, answer
// keys, and answer sheets.
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
	templateMu     sync.RWMutex
	templateDir    = defaultTemplateDir()
	latexTemplates *texttemplate.Template
)

// requiredTemplateNames is the contract of the external template directory.
// A malformed configuration should fail at startup, not during rendering.
var requiredTemplateNames = []string{
	"answer_block",
	"answer_sheet",
	"answer_sheet_bubble_grid",
	"answer_sheet_bubble_group",
	"answer_sheet_info_row",
	"answer_sheet_notice",
	"answers_document",
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
	s = normalizeDisplayMathWhitespace(s)
	s = normalizeFillBlankMarkers(s)
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
		return b.String()
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
		if r == '\\' && i+1 < len(runes) && runes[i+1] == '$' && mathDelimiter == "" {
			b.WriteString(`\$`)
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
	if layout == optionsInline2 {
		perRow = 2
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
	flush := func() {
		if len(fragments) > 0 {
			out = append(out, strings.Join(fragments, "  "))
			fragments = fragments[:0]
		}
	}
	for _, line := range kept {
		trimmed := strings.TrimSpace(line)
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
			(i+2 >= len(result) || !isASCIILetter(result[i+2])) {
			normalized.WriteByte('\n')
			i++
			continue
		}
		normalized.WriteByte(result[i])
	}
	return strings.TrimSpace(normalized.String())
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

type AnswerEntry struct {
	Number      int
	Answer      string
	RubricItems []string
}

type answerBlockTemplateData struct {
	Number      int
	Answer      string
	RubricItems []string
}

func answerView(number int, answer string, rubricItems []string) answerBlockTemplateData {
	items := make([]string, len(rubricItems))
	for i, item := range rubricItems {
		items[i] = escapeLaTeX(item)
	}
	return answerBlockTemplateData{Number: number, Answer: escapeLaTeX(answer), RubricItems: items}
}

func AnswerKey(meta ExamMeta, answers []AnswerEntry, footer FooterBranding) string {
	headLine := "全国硕士研究生入学统一考试"
	if meta.Year != "" {
		headLine = meta.Year + " 年" + headLine
	}
	views := make([]answerBlockTemplateData, len(answers))
	for i, answer := range answers {
		views[i] = answerView(answer.Number, answer.Answer, answer.RubricItems)
	}
	return renderTemplate("answers_document", struct {
		Footer   footerTemplateData
		HeadLine string
		Answers  []answerBlockTemplateData
	}{
		Footer:   footerTemplateData{Left: escapeLaTeX(footer.Left), Right: escapeLaTeX(footer.Right)},
		HeadLine: escapeLaTeX(headLine), Answers: views,
	})
}

func AnswerBlock(index int, answer string, rubricItems []string) string {
	return renderTemplate("answer_block", answerView(index, answer, rubricItems))
}
