package latex

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TexDiagnostic is one source or compiler diagnostic related to LaTeX
// rendering. A successful TeX exit is not enough to guarantee that every
// glyph was emitted, so callers should inspect Diagnostics as well.
type TexDiagnostic struct {
	Severity string `json:"severity"` // error | warning
	Kind     string `json:"kind"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
}

// InspectTeX performs inexpensive checks before invoking a TeX engine.
// It catches unmatched math delimiters/environments and content that is
// commonly produced when an LLM intended math but returned plain text.
func InspectTeX(source string) []TexDiagnostic {
	var diagnostics []TexDiagnostic
	mathMode := ""
	envStack := make([]texEnvironment, 0)
	coveredUnicode := unicodeDeclarations(source)
	dollarCount := countUnescapedDollars(source)
	dollarsUsable := dollarCount%2 == 0

	for lineNo, raw := range strings.Split(source, "\n") {
		line := stripTeXComment(raw)
		if line == "" {
			continue
		}
		for _, match := range texEnvironmentRE.FindAllStringSubmatchIndex(line, -1) {
			kind := line[match[2]:match[3]]
			name := line[match[4]:match[5]]
			if kind == "begin" {
				envStack = append(envStack, texEnvironment{name: name, line: lineNo + 1})
				continue
			}
			if len(envStack) == 0 {
				diagnostics = append(diagnostics, TexDiagnostic{
					Severity: "error", Kind: "unmatched-environment", Line: lineNo + 1,
					Message: fmt.Sprintf("\\end{%s} 没有对应的 \\begin{%s}", name, name),
				})
				continue
			}
			last := envStack[len(envStack)-1]
			if last.name != name {
				diagnostics = append(diagnostics, TexDiagnostic{
					Severity: "error", Kind: "mismatched-environment", Line: lineNo + 1,
					Message: fmt.Sprintf("\\end{%s} 与 \\begin{%s} 不匹配（第 %d 行开始）", name, last.name, last.line),
				})
				continue
			}
			envStack = envStack[:len(envStack)-1]
		}

		for i := 0; i < len(line); i++ {
			if line[i] != '\\' || i+1 >= len(line) {
				continue
			}
			if line[i+1] == '\\' {
				i++
				continue
			}
			if mathDelimiter := line[i : i+2]; mathDelimiter == `\(` || mathDelimiter == `\[` {
				if mathMode != "" {
					diagnostics = append(diagnostics, TexDiagnostic{
						Severity: "error", Kind: "nested-math", Line: lineNo + 1,
						Message: fmt.Sprintf("数学分隔符 %s 出现在未关闭的 %s 内", mathDelimiter, mathMode),
					})
				} else {
					mathMode = mathDelimiter
				}
				i++
				continue
			}
			if mathDelimiter := line[i : i+2]; mathDelimiter == `\)` || mathDelimiter == `\]` {
				want := `\(`
				if mathDelimiter == `\]` {
					want = `\[`
				}
				if mathMode != want {
					diagnostics = append(diagnostics, TexDiagnostic{
						Severity: "error", Kind: "unmatched-math-delimiter", Line: lineNo + 1,
						Message: fmt.Sprintf("数学分隔符 %s 没有对应的 %s", mathDelimiter, want),
					})
				} else {
					mathMode = ""
				}
				i++
				continue
			}
			if line[i+1] == '$' {
				i++
				continue
			}
		}

		for i := 0; i < len(line); i++ {
			if line[i] != '$' || (i > 0 && line[i-1] == '\\') {
				continue
			}
			if !dollarsUsable {
				continue
			}
			if i+1 < len(line) && line[i+1] == '$' {
				if mathMode == "" {
					mathMode = "$$"
					i++
				} else if mathMode == "$$" {
					mathMode = ""
					i++
				} else if mathMode == "$" {
					// The first dollar closes the inline expression; leave the
					// second one for the next iteration as a new opener. This
					// handles adjacent expressions such as $a$$b$.
					mathMode = ""
				} else {
					diagnostics = append(diagnostics, TexDiagnostic{
						Severity: "error", Kind: "nested-math", Line: lineNo + 1,
						Message: "$$ 出现在未关闭的其他数学分隔符内",
					})
				}
				i++
				continue
			}
			if mathMode == "" {
				mathMode = "$"
			} else if mathMode == "$" {
				mathMode = ""
			} else {
				diagnostics = append(diagnostics, TexDiagnostic{
					Severity: "error", Kind: "nested-math", Line: lineNo + 1,
					Message: "$ 出现在未关闭的其他数学分隔符内",
				})
			}
		}

		if strings.Contains(line, `\textasciicircum`) || strings.Contains(line, `\textasciitilde`) {
			diagnostics = append(diagnostics, TexDiagnostic{
				Severity: "warning", Kind: "escaped-math-symbol", Line: lineNo + 1,
				Message: "检测到被转义的 ^ 或 ~；请确认它不是被误当作普通文本的数学表达式",
			})
		}
		for _, r := range line {
			if !isLikelyMathRune(r) || coveredUnicode[r] {
				continue
			}
			diagnostics = append(diagnostics, TexDiagnostic{
				Severity: "warning", Kind: "unicode-math-rune", Line: lineNo + 1,
				Message: fmt.Sprintf("源文件含未声明的数学字符 %q（U+%04X）", r, r),
			})
		}
	}

	if mathMode != "" {
		diagnostics = append(diagnostics, TexDiagnostic{
			Severity: "error", Kind: "unclosed-math", Message: fmt.Sprintf("未关闭的数学分隔符 %s", mathMode),
		})
	}
	for _, env := range envStack {
		diagnostics = append(diagnostics, TexDiagnostic{
			Severity: "error", Kind: "unclosed-environment", Line: env.line,
			Message: fmt.Sprintf("\\begin{%s} 没有对应的 \\end{%s}", env.name, env.name),
		})
	}
	if !dollarsUsable {
		diagnostics = append(diagnostics, TexDiagnostic{
			Severity: "error", Kind: "unmatched-dollar", Message: "源文件中的 $ 数量为奇数，至少有一个数学分隔符未闭合",
		})
	}
	return diagnostics
}

// InspectTeXLog converts the common non-fatal TeX messages into structured
// diagnostics. Missing glyphs are errors because the PDF can look complete
// while silently losing symbols.
func InspectTeXLog(log string) []TexDiagnostic {
	var diagnostics []TexDiagnostic
	lineRE := regexp.MustCompile(`at lines? ([0-9]+)`)
	for _, raw := range strings.Split(log, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.Contains(line, `Overfull \hbox`) && strings.Contains(line, "too wide") {
			diagnostics = append(diagnostics, TexDiagnostic{"warning", "overfull-hbox", 0, line})
			continue
		}
		lineNo := 0
		if match := lineRE.FindStringSubmatch(line); len(match) == 2 {
			lineNo, _ = strconv.Atoi(match[1])
		}
		switch {
		case strings.Contains(line, "Missing character:"):
			diagnostics = append(diagnostics, TexDiagnostic{"error", "missing-character", lineNo, line})
		case strings.Contains(line, `Overfull \hbox`):
			diagnostics = append(diagnostics, TexDiagnostic{"warning", "overfull-hbox", lineNo, line})
		case strings.Contains(line, `Underfull \hbox`):
			diagnostics = append(diagnostics, TexDiagnostic{"warning", "underfull-hbox", lineNo, line})
		case strings.Contains(line, "Undefined control sequence"), strings.Contains(line, "Emergency stop"), strings.Contains(line, "Runaway argument"), strings.HasPrefix(line, "!"):
			diagnostics = append(diagnostics, TexDiagnostic{"error", "tex-error", lineNo, line})
		case strings.Contains(line, "LaTeX Warning:"), strings.Contains(line, "Package ") && strings.Contains(line, "Warning:"):
			diagnostics = append(diagnostics, TexDiagnostic{"warning", "tex-warning", lineNo, line})
		}
	}
	return diagnostics
}

func hasDiagnosticError(diagnostics []TexDiagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			return true
		}
	}
	return false
}

type texEnvironment struct {
	name string
	line int
}

var texEnvironmentRE = regexp.MustCompile(`\\(begin|end)\s*\{([^{}]+)\}`)

func stripTeXComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] != '%' || (i > 0 && line[i-1] == '\\') {
			continue
		}
		return line[:i]
	}
	return line
}

func countUnescapedDollars(source string) int {
	count := 0
	for _, line := range strings.Split(source, "\n") {
		line = stripTeXComment(line)
		for i := 0; i < len(line); i++ {
			if line[i] == '$' && (i == 0 || line[i-1] != '\\') {
				count++
			}
		}
	}
	return count
}

func unicodeDeclarations(source string) map[rune]bool {
	covered := make(map[rune]bool)
	for _, match := range regexp.MustCompile(`\\newunicodechar\{(.)(?:\})`).FindAllStringSubmatch(source, -1) {
		if len(match) == 2 {
			covered[[]rune(match[1])[0]] = true
		}
	}
	return covered
}

func isLikelyMathRune(r rune) bool {
	switch r {
	case '∈', '∉', '∀', '∃', '∂', '∫', '∑', '∏', '√', '∛', '∞', '≤', '≥', '≠', '±', '∪', '∩',
		'α', 'β', 'γ', 'δ', 'ε', 'θ', 'λ', 'μ', 'π', 'σ', 'φ', 'ω', 'Ω', '×', '·', '→', '←', '↔',
		'⁰', '¹', '²', '³', '⁴', '⁵', '⁶', '⁷', '⁸', '⁹', '₀', '₁', '₂', '₃', '₄', '₅', '₆', '₇', '₈', '₉':
		return true
	default:
		return false
	}
}
