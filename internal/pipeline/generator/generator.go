// Package generator implements Stage [4]: 逐题生成层.
//
// LaTeX templates are structural scaffolds (管格式/答题区/分值标头/装配),
// NOT per-question fill-slots. LLM generates each question's content.
// Each prompt carries few-shot examples from real exams for style alignment.
//
// Model selection by spec type:
//   - choice/fill_blank: fast model (deepseek-chat)
//   - major(证明/计算): reasoning-strong model (glm-4)
//   - major(算法): coder系 model
package generator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/choice"
	"GoT0GenPaper/internal/pipeline/quality"
)

// Generator produces questions from spec entries using LLM + templates.
type Generator struct {
	LLM       *llm.Client
	Templates map[string]string // templateKey → LaTeX skeleton (structural only)
	// FewShotExamples provides real-exam examples for style alignment.
	// Keyed by (type + "_" + first-point) or by type alone as fallback.
	FewShotExamples map[string][]string // templateKey → []example stems
	// ModelByType selects the LLM model for each question type.
	ModelByType map[string]string
	// DefaultModel is the fallback when no type-specific model is set.
	DefaultModel string
	// FallbackModel is tried once (cross-family) when the primary model's
	// response fails — flaky gateways drop or truncate responses.
	FallbackModel string
	// ArticleSource selects how English passages are obtained: written by the
	// model (default) or fetched from the web.
	ArticleSource ArticleSourceOptions
	// articleHistory remembers the passages already written for the current
	// paper so later sections pick different topics instead of repeating the
	// first one (each section is generated in a separate request).
	articleHistory []articleHistoryEntry
}

// articleHistoryEntry is one passage already used in this paper run.
type articleHistoryEntry struct {
	topic   string
	opening string
}

// New creates a Generator.
func New(llmClient *llm.Client, templates map[string]string) *Generator {
	return &Generator{
		LLM:             llmClient,
		Templates:       templates,
		FewShotExamples: make(map[string][]string),
		ModelByType:     make(map[string]string),
		DefaultModel:    "deepseek-chat",
	}
}

// SetFewShot attaches real-exam example stems for style alignment.
func (g *Generator) SetFewShot(examples map[string][]string) {
	g.FewShotExamples = examples
}

// generationResponse is the LLM JSON output for a generated question.
type generationResponse struct {
	Stem           string   `json:"stem"`       // 题面 in LaTeX
	Options        []string `json:"options"`    // non-empty for choice questions
	Answer         string   `json:"answer"`     // 参考答案 in LaTeX
	Difficulty     float64  `json:"difficulty"` // LLM self-estimated [0,1]
	SharedMaterial string   `json:"sharedMaterial,omitempty"`
	SharedOptions  []string `json:"sharedOptions,omitempty"`
}

type english2GroupItem struct {
	SpecID     string   `json:"specId"`
	Stem       string   `json:"stem"`
	Options    []string `json:"options"`
	Answer     string   `json:"answer"`
	Difficulty float64  `json:"difficulty"`
}

type english2GroupResponse struct {
	SharedMaterial string              `json:"sharedMaterial"`
	SharedOptions  []string            `json:"sharedOptions"`
	Items          []english2GroupItem `json:"items"`
}

var (
	englishGroupWordPattern     = regexp.MustCompile(`(?i)[a-z]+(?:['’][a-z]+)?`)
	englishClozeMarkerPattern   = regexp.MustCompile(`\[(\d{1,2})\]`)
	partBStatementPrefixPattern = regexp.MustCompile(`(?i)^[A-G][.)、:]\s*`)
	partBPersonNamePattern      = regexp.MustCompile(`^[A-Z][A-Za-z'’.-]+(?:\s+[A-Z][A-Za-z'’.-]+){1,4}$`)
	microsecondUnitPattern      = regexp.MustCompile(`(^|[^A-Za-z0-9])([0-9]+(?:\.[0-9]+)?)\\mu\s*s\b`)
	addressBusRangePattern      = regexp.MustCompile(`\bA([0-9]+)\\sim\s*A([0-9]+)\b`)
	overlineMathPattern         = regexp.MustCompile(`\\overline\{([^{}]+)\}`)
	registerRangePattern        = regexp.MustCompile(`\bR([0-9]+)~R([0-9]+)\b`)
	// Models sometimes fill a fill-in-the-blank slot with tildes, which render
	// as literal "~~~~~" instead of a blank underline. The assembler turns each
	// tilde into \textasciitilde{}, so both the stored and the rendered form are
	// recognized.
	tildeBlankPattern = regexp.MustCompile(`\\underline\{(?:~{2,}|(?:\\textasciitilde\{\}){2,})\}`)
)

// Generate produces a single question from a spec entry.
//
// The prompt includes:
//   - the spec entry (type, points, cognitive, difficulty band, score)
//   - few-shot examples from real exams (if available)
//   - the LaTeX template skeleton (structural scaffold)
//   - instructions to produce stem + options + answer in LaTeX
func (g *Generator) Generate(ctx context.Context, spec models.SpecEntry) (models.GeneratedQuestion, error) {
	return g.GenerateForSubject(ctx, "", spec)
}

// GenerateForSubject generates one question while applying the subject's
// syllabus constraints before and after the model call.
func (g *Generator) GenerateForSubject(ctx context.Context, subject string, spec models.SpecEntry) (models.GeneratedQuestion, error) {
	if strings.TrimSpace(spec.ID) == "" {
		return models.GeneratedQuestion{}, fmt.Errorf("spec ID 不能为空")
	}
	if !finitePositive(spec.Score) {
		return models.GeneratedQuestion{}, fmt.Errorf("spec %s 分值必须大于 0", spec.ID)
	}
	switch spec.Type {
	case models.TypeChoice, models.TypeMultiChoice, models.TypeFillBlank, models.TypeMajor:
	default:
		return models.GeneratedQuestion{}, fmt.Errorf("spec %s 使用未知题型 %q", spec.ID, spec.Type)
	}
	if issues := quality.SubjectContentIssues(subject, spec.Points, ""); len(issues) > 0 {
		return models.GeneratedQuestion{}, fmt.Errorf("spec %s 超出科目考纲：%s", spec.ID, strings.Join(issues, "；"))
	}
	q := models.GeneratedQuestion{
		ID:     fmt.Sprintf("gen_%s", spec.ID),
		SpecID: spec.ID,
		Type:   spec.Type,
		Points: spec.Points,
		Score:  spec.Score,
		Status: "needs_review",
	}

	if g.LLM == nil {
		// Without LLM, produce a placeholder so the pipeline can still flow.
		q.Stem = fmt.Sprintf("%% TODO: 生成 %s 题面 (知识点: %s, 分值: %.0f)",
			spec.Type, strings.Join(spec.Points, ", "), spec.Score)
		q.Answer = "% TODO: 参考答案"
		q.Difficulty = bandToFloat(spec.DiffBand)
		q.Warnings = []string{"LLM不可用，题面和答案是占位内容"}
		return q, nil
	}

	// Build the system prompt.
	system := g.buildSystemPromptForSubject(subject, spec)

	// Build the user prompt with spec + few-shot + template.
	user := g.buildUserPrompt(spec)

	// Call LLM with JSON mode for structured output.
	var resp generationResponse
	model := g.selectModel(spec.Type)
	if err := g.LLM.ChatJSON(ctx, model, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, &resp); err != nil {
		// Flaky channel / truncation — one cross-family retry with the
		// judge model before falling back to a placeholder.
		if fb := g.FallbackModel; fb != "" && fb != model {
			if retryErr := g.LLM.ChatJSON(ctx, fb, []llm.Message{
				{Role: "system", Content: system},
				{Role: "user", Content: user},
			}, &resp); retryErr == nil {
				return g.finishForSubject(subject, spec, resp)
			}
		}
		// Fallback: produce a minimal question so the pipeline continues.
		// The placeholder must stay single-line LaTeX-safe: embedding the
		// raw response here once crashed the exam compile (raw \begin{array}
		// inside a text placeholder).
		q.Stem = fmt.Sprintf("%% LLM 生成失败 (%s), 需手动补充 %s 题面", oneLine(err), spec.Type)
		q.Answer = "% TODO: 参考答案 (LLM 失败)"
		q.Difficulty = bandToFloat(spec.DiffBand)
		q.Warnings = []string{"LLM生成失败，题目需要人工补充"}
		return q, nil
	}

	question, err := g.finishForSubject(subject, spec, resp)
	if err != nil || question.Status == "ready" {
		return question, err
	}

	// Retry once when the model answered with the wrong shape or invalid fields.
	retryUser := user + "\n\n格式校验失败，请重新生成整题。必须遵守系统提示中的题型及 JSON 字段要求。校验问题：" + strings.Join(question.Warnings, "；")
	var retryResp generationResponse
	if retryErr := g.LLM.ChatJSON(ctx, model, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: retryUser},
	}, &retryResp); retryErr == nil {
		retried, finishErr := g.finishForSubject(subject, spec, retryResp)
		if finishErr != nil || retried.Status == "ready" {
			return retried, finishErr
		}
		question.Warnings = append(question.Warnings, "格式修正重试仍未通过："+strings.Join(retried.Warnings, "；"))
	} else {
		question.Warnings = append(question.Warnings, "格式修正重试失败："+oneLine(retryErr))
	}
	return question, nil
}

// finish assembles the GeneratedQuestion from a successful LLM response.
func (g *Generator) finish(spec models.SpecEntry, resp generationResponse) (models.GeneratedQuestion, error) {
	return g.finishForSubject("", spec, resp)
}

func (g *Generator) finishForSubject(subject string, spec models.SpecEntry, resp generationResponse) (models.GeneratedQuestion, error) {
	q := models.GeneratedQuestion{
		ID:     fmt.Sprintf("gen_%s", spec.ID),
		SpecID: spec.ID,
		Type:   spec.Type,
		Points: spec.Points,
		Score:  spec.Score,
	}
	q.Stem = cleanGeneratedStem(NormalizeGeneratedLaTeX(resp.Stem))
	q.Options = normalizeOptions(resp.Options)
	q.SharedMaterial = cleanGeneratedStem(NormalizeGeneratedLaTeX(resp.SharedMaterial))
	q.SharedOptions = normalizeOptions(resp.SharedOptions)
	q.Answer = SyncAnswerSummary(NormalizeGeneratedLaTeX(resp.Answer))
	for i := range q.Options {
		q.Options[i] = NormalizeGeneratedLaTeX(q.Options[i])
	}
	for i := range q.SharedOptions {
		q.SharedOptions[i] = NormalizeGeneratedLaTeX(q.SharedOptions[i])
	}
	q.Difficulty = resp.Difficulty
	q.Status = "ready"
	if math.IsNaN(q.Difficulty) || math.IsInf(q.Difficulty, 0) || q.Difficulty <= 0 || q.Difficulty > 1 {
		q.Difficulty = bandToFloat(spec.DiffBand)
		q.Warnings = append(q.Warnings, "模型未提供有效难度，已使用目标难度估计")
	}
	// Self-reported difficulty is useful as a signal, but models frequently
	// collapse every item to 0.5. Keep the paper's requested difficulty
	// distribution stable by snapping an out-of-band estimate to the target
	// band's center; the prompt still asks the model to make the mathematics
	// itself match that band.
	if calibrated := calibrateDifficulty(spec.DiffBand, q.Difficulty); calibrated != q.Difficulty {
		q.Warnings = append(q.Warnings, fmt.Sprintf("模型难度 %.2f 偏离题位目标，已校准为 %.2f", q.Difficulty, calibrated))
		q.Difficulty = calibrated
	}
	if blankOrPlaceholder(q.Stem) || blankOrPlaceholder(q.Answer) {
		q.Status = "needs_review"
		q.Warnings = append(q.Warnings, "题面或参考答案为空或为占位内容")
	}
	if spec.Type == models.TypeChoice || spec.Type == models.TypeMultiChoice {
		wantOptions := quality.ExpectedChoiceOptionCount(q)
		if len(q.Options) != wantOptions {
			q.Status = "needs_review"
			q.Warnings = append(q.Warnings, fmt.Sprintf("选择题选项数量为 %d，期望 %d", len(q.Options), wantOptions))
		} else {
			seenOptions := make(map[string]struct{}, len(q.Options))
			for i, option := range q.Options {
				key := strings.TrimSpace(option)
				if key == "" {
					q.Status = "needs_review"
					q.Warnings = append(q.Warnings, fmt.Sprintf("选择题选项 %d 为空", i+1))
					continue
				}
				if _, exists := seenOptions[key]; exists {
					q.Status = "needs_review"
					q.Warnings = append(q.Warnings, "选择题包含重复选项")
				}
				seenOptions[key] = struct{}{}
			}
			labels := answerChoiceLabels(q.Answer)
			alphabet := "ABCD"
			if models.IsEnglishPartB(q.Points) {
				alphabet = "ABCDEFG"
				labels = choice.ParseWithAlphabet(q.Answer, alphabet)
			}
			if issue := choice.ValidateWithAlphabet(labels, spec.Type == models.TypeMultiChoice, alphabet); issue != "" {
				q.Status = "needs_review"
				q.Warnings = append(q.Warnings, "选择题参考答案无法解析为合法选项")
			}
		}
	}
	if issues := quality.Issues(q); len(issues) > 0 {
		q.Status = "needs_review"
		q.Warnings = append(q.Warnings, issues...)
	}
	content := strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n")
	if issues := quality.SubjectContentIssues(subject, q.Points, content); len(issues) > 0 {
		q.Status = "needs_review"
		q.Warnings = append(q.Warnings, issues...)
	}
	return q, nil
}

func answerChoiceLabels(raw string) []string {
	return choice.Parse(raw)
}

// normalizeGeneratedNewlines repairs models that double-escape line breaks
// inside JSON string values. A single backslash remains untouched so LaTeX
// commands such as \nabla keep their meaning.
func normalizeGeneratedNewlines(value string) string {
	value = strings.ReplaceAll(value, `\\r\\n`, "\n")
	value = strings.ReplaceAll(value, `\\n`, "\n")
	value = strings.ReplaceAll(value, `\\r`, "\n")
	return value
}

// NormalizeGeneratedLaTeX repairs common model LaTeX output defects before
// content is persisted or rendered. It is also used when reassembling papers
// generated by older versions of the CLI.
func NormalizeGeneratedLaTeX(value string) string {
	value = normalizeGeneratedNewlines(value)
	value = moveCJKPunctuationOutOfMath(value)
	value = smartenQuotes(value)
	value = tildeBlankPattern.ReplaceAllString(value, `\underline{\quad\quad}`)
	value = registerRangePattern.ReplaceAllString(value, "R$1 至 R$2")
	// A single-escaped JSON sequence such as \thicksim can be decoded as a
	// tab followed by "hicksim". Restore the intended math command before the
	// general text escaper replaces control characters.
	value = strings.ReplaceAll(value, "$\thicksim$", "$\\sim$")
	value = wrapBareMathMatches(value, addressBusRangePattern, func(groups []string) string {
		return fmt.Sprintf(`$A_{%s}\sim A_{%s}$`, groups[1], groups[2])
	})
	value = wrapBareMathMatches(value, overlineMathPattern, func(groups []string) string {
		return fmt.Sprintf(`$\overline{%s}$`, groups[1])
	})
	matches := microsecondUnitPattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	var out strings.Builder
	last := 0
	for _, match := range matches {
		numberStart, numberEnd := match[4], match[5]
		unitEnd := match[1]
		out.WriteString(value[last:numberStart])
		number := value[numberStart:numberEnd]
		if inMathMode(value, numberStart) {
			out.WriteString(number + `\,\mu\mathrm{s}`)
		} else {
			out.WriteString(`$` + number + `\,\mu\mathrm{s}$`)
		}
		last = unitEnd
	}
	out.WriteString(value[last:])
	return out.String()
}

// smartenQuotes turns paired ASCII double quotes in prose into LaTeX open/close
// quotes. xelatex renders a bare `"` with the font's double-quote glyph, so
// `"changed their minds"` prints as two closing quotes (”changed their
// minds”). Math segments are left untouched: `"` there is a literal, and
// “/” would be wrong.
func smartenQuotes(value string) string {
	value = repairBacktickQuotes(value)
	if strings.Count(value, `"`) < 2 {
		return value
	}
	var out strings.Builder
	open := true
	for i := 0; i < len(value); {
		if open2, _, ok := mathDelimiterAt(value, i); ok {
			// Copy the whole math segment verbatim.
			bodyStart := i + len(open2)
			if rel := strings.Index(value[bodyStart:], mathCloserFor(open2)); rel >= 0 {
				end := bodyStart + rel + len(mathCloserFor(open2))
				out.WriteString(value[i:end])
				i = end
				continue
			}
		}
		if value[i] == '"' {
			// \textquotedblleft/right instead of ``/'' : the assembler's markdown
			// cleaner treats a leading double backtick as a code span and strips it.
			if open {
				out.WriteString(`\textquotedblleft `)
			} else {
				out.WriteString(`\textquotedblright `)
			}
			open = !open
			i++
			continue
		}
		out.WriteByte(value[i])
		i++
	}
	return out.String()
}

// repairBacktickQuotes rewrites the “…” quote pairs an earlier normaliser
// emitted. The assembler's markdown cleaner treats a leading double backtick as
// a code span and strips it, which silently deleted the opening quote; the
// explicit commands survive both the cleaner and the escaper.
func repairBacktickQuotes(value string) string {
	for {
		start := strings.Index(value, "``")
		if start < 0 {
			return value
		}
		rest := value[start+2:]
		if end := strings.Index(rest, "''"); end >= 0 {
			value = value[:start] + `\textquotedblleft ` + rest[:end] + `\textquotedblright ` + rest[end+2:]
			continue
		}
		value = value[:start] + `\textquotedblleft ` + rest
	}
}

// mathCloserFor returns the closing delimiter that matches an opening one.
func mathCloserFor(open string) string {
	switch open {
	case "$$":
		return "$$"
	case `\[`:
		return `\]`
	case `\(`:
		return `\)`
	default:
		return "$"
	}
}

// cjkMathPunctuation is the set of CJK punctuation marks models sometimes leave
// just inside math delimiters when they write prose around a formula.
const cjkMathPunctuation = "。，、；：？！（）〔〕【】《》〈〉“”‘’…—～·"

// moveCJKPunctuationOutOfMath moves CJK punctuation sitting immediately inside
// an *inline* math delimiter to the outside, so the punctuation is typeset as
// text (with the CJK font and correct spacing) instead of as part of the
// formula: `$（x）$` becomes `（$x$）`.
//
// Display math is handled the other way round: the preamble enables CJKmath, so
// a trailing `。` renders correctly inside `$$…$$`, and punctuation that an
// older pipeline version pushed outside a display segment is pulled back in.
// Leaving it outside would start the next line with the punctuation, which
// Chinese typography forbids.
func moveCJKPunctuationOutOfMath(value string) string {
	if !strings.ContainsAny(value, cjkMathPunctuation) {
		return value
	}
	var out strings.Builder
	for i := 0; i < len(value); {
		open, closer, ok := mathDelimiterAt(value, i)
		if !ok {
			out.WriteByte(value[i])
			i++
			continue
		}
		bodyStart := i + len(open)
		rel := strings.Index(value[bodyStart:], closer)
		if rel < 0 {
			out.WriteString(open)
			i = bodyStart
			continue
		}
		bodyEnd := bodyStart + rel
		if open != "$" && open != `\(` {
			// Display math: pull back punctuation that a previous version moved
			// outside the segment (see the doc comment).
			trailing := value[bodyEnd+len(closer):]
			pulled := trailing[:len(trailing)-len(strings.TrimLeft(trailing, cjkMathPunctuation))]
			out.WriteString(open)
			out.WriteString(value[bodyStart:bodyEnd])
			out.WriteString(pulled)
			out.WriteString(closer)
			i = bodyEnd + len(closer) + len(pulled)
			continue
		}
		body := value[bodyStart:bodyEnd]
		lead := body[:len(body)-len(strings.TrimLeft(body, cjkMathPunctuation))]
		core := body[len(lead):]
		trailLen := len(core) - len(strings.TrimRight(core, cjkMathPunctuation))
		trail := core[len(core)-trailLen:]
		core = core[:len(core)-trailLen]

		out.WriteString(lead)
		out.WriteString(open)
		out.WriteString(core)
		out.WriteString(closer)
		out.WriteString(trail)
		i = bodyEnd + len(closer)
	}
	return out.String()
}

// mathDelimiterAt reports whether value[i:] opens a math segment and returns the
// matching closing delimiter. Escaped dollars and LaTeX line breaks such as
// `\\[2mm]` are not treated as delimiters.
func mathDelimiterAt(value string, i int) (open, closer string, ok bool) {
	escaped := i > 0 && value[i-1] == '\\'
	switch {
	case strings.HasPrefix(value[i:], "$$") && !escaped:
		return "$$", "$$", true
	case value[i] == '$' && !escaped:
		return "$", "$", true
	case strings.HasPrefix(value[i:], `\[`) && !escaped:
		return `\[`, `\]`, true
	case strings.HasPrefix(value[i:], `\(`) && !escaped:
		return `\(`, `\)`, true
	}
	return "", "", false
}

// SyncAnswerSummary makes the leading answer line agree with the authoritative
// answer the model states after its derivation.
//
// Models write the answer field top-down: they put a summary first and only then
// work out the result, so the leading summary is a guess that frequently
// contradicts the derivation (reviewed math2 papers showed a wrong summary in
// two of three answer questions). The prompt therefore asks for a final
// `最终答案：…` line after the derivation; this function copies that line's
// payload into the leading `答案：…` summary. Nothing changes when the model did
// not write a trailing authoritative line.
func SyncAnswerSummary(answer string) string {
	lines := strings.Split(answer, "\n")
	head, headLine := -1, answerSummaryLine{}
	for i, line := range lines {
		parsed, ok := parseAnswerSummaryLine(line)
		if ok && !finalAnswerLine(line) {
			head, headLine = i, parsed
			break
		}
	}
	tailPayload := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if !finalAnswerLine(lines[i]) {
			continue
		}
		if parsed, ok := parseAnswerSummaryLine(lines[i]); ok {
			tailPayload = parsed.payload
			break
		}
	}
	if tailPayload == "" || headLine.payload == tailPayload {
		return answer
	}
	if head < 0 {
		// The model only stated the answer after its derivation; surface it at the
		// top as well, which is where the answer key is read from.
		return "答案：" + tailPayload + "\n\n" + answer
	}
	lines[head] = headLine.render(tailPayload)
	return strings.Join(lines, "\n")
}

// answerSummaryLine is one parsed "答案：…" style summary line. The pieces are
// kept separately so a sync can replace the payload while preserving the
// model's own indentation and markdown emphasis.
type answerSummaryLine struct {
	indent  string
	lead    string
	label   string // label including its colon, e.g. "答案："
	payload string
	trail   string
}

func (l answerSummaryLine) render(payload string) string {
	trail := l.trail
	if l.lead != "" && trail == "" {
		// The model bolded the label alone; close the emphasis so the markdown
		// stays balanced after the payload is replaced.
		trail = l.lead
	}
	return l.indent + l.lead + l.label + payload + trail
}

// parseAnswerSummaryLine splits a leading/trailing summary line into its parts,
// accepting the markdown emphasis models like to add.
func parseAnswerSummaryLine(line string) (answerSummaryLine, bool) {
	trimmed := strings.TrimSpace(line)
	indent := strings.TrimSuffix(line, strings.TrimLeft(line, " \t"))
	lead := ""
	for strings.HasPrefix(trimmed, "*") {
		lead += "*"
		trimmed = strings.TrimPrefix(trimmed, "*")
	}
	trail := ""
	for strings.HasSuffix(trimmed, "*") {
		trail = "*" + trail
		trimmed = strings.TrimSuffix(trimmed, "*")
	}
	trimmed = strings.TrimSpace(trimmed)
	for _, label := range []string{"最终答案", "参考答案", "答案"} {
		if !strings.HasPrefix(trimmed, label) {
			continue
		}
		rest := strings.TrimSpace(trimmed[len(label):])
		// Models also bold the label alone ("**答案**：…"); drop that emphasis
		// before looking for the colon.
		for strings.HasPrefix(rest, "*") {
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "*"))
		}
		colon := ""
		for _, sep := range []string{"：", ":"} {
			if strings.HasPrefix(rest, sep) {
				colon = sep
				break
			}
		}
		if colon == "" {
			continue
		}
		// Slicing with len(sep) keeps the fullwidth colon intact.
		payload := strings.TrimSpace(rest[len(colon):])
		if payload == "" {
			continue
		}
		return answerSummaryLine{indent: indent, lead: lead, label: label + colon, payload: payload, trail: trail}, true
	}
	return answerSummaryLine{}, false
}

func finalAnswerLine(line string) bool {
	parsed, ok := parseAnswerSummaryLine(line)
	if !ok {
		return false
	}
	return strings.HasPrefix(parsed.label, "最终答案") || strings.HasPrefix(parsed.label, "参考答案")
}

func wrapBareMathMatches(value string, pattern *regexp.Regexp, render func([]string) string) string {
	matches := pattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	var out strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		out.WriteString(value[last:start])
		if inMathMode(value, start) {
			out.WriteString(value[start:end])
		} else {
			groups := make([]string, 0, len(match)/2)
			for i := 0; i+1 < len(match); i += 2 {
				if match[i] < 0 {
					groups = append(groups, "")
					continue
				}
				groups = append(groups, value[match[i]:match[i+1]])
			}
			out.WriteString(render(groups))
		}
		last = end
	}
	out.WriteString(value[last:])
	return out.String()
}

func inMathMode(value string, before int) bool {
	inside := false
	for i := 0; i < before; i++ {
		if value[i] != '$' {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && value[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			inside = !inside
		}
	}
	return inside
}

func calibrateDifficulty(band models.DifficultyBand, value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return bandToFloat(band)
	}
	low, high := 0.0, 1.0
	switch band {
	case models.BandEasy:
		high = 0.34
	case models.BandMedium:
		low, high = 0.35, 0.64
	case models.BandHard:
		low = 0.65
	}
	if value < low || value > high {
		return bandToFloat(band)
	}
	return value
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func blankOrPlaceholder(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || strings.HasPrefix(trimmed, "%") || strings.Contains(strings.ToUpper(trimmed), "TODO")
}

// oneLine flattens an error for safe embedding in a LaTeX comment.
func oneLine(err error) string {
	s := strings.ReplaceAll(err.Error(), "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 150 {
		s = s[:150]
	}
	return s
}

// cleanGeneratedStem strips artifacts LLMs add to the stem: \question
// wrappers (numbering/scaffolding belongs to the assembler), template
// placeholders and document wrappers.
func cleanGeneratedStem(stem string) string {
	lines := strings.Split(stem, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, `\question`) || strings.HasPrefix(t, `\begin{document}`) ||
			strings.HasPrefix(t, `\end{document}`) || strings.HasPrefix(t, `\documentclass`) {
			continue
		}
		kept = append(kept, t)
	}
	return strings.Join(kept, "\n")
}

// normalizeOptions repairs LLM option formatting: a single string holding
// every option ("A. x B. y C. z D. w") is split into four; embedded labels
// are left to the assembler's renderer to strip.
func normalizeOptions(options []string) []string {
	if len(options) == 1 {
		parts := splitLabeledOptions(options[0])
		if len(parts) >= 2 {
			return parts
		}
	}
	return options
}

// splitLabeledOptions splits "A. x B. y C. z D. w" on option labels.
func splitLabeledOptions(s string) []string {
	re := regexp.MustCompile(`(?:^|\s)[B-H][\.、．)）]\s*`)
	idxs := re.FindAllStringIndex(s, -1)
	// The first label (A.) anchors the start only if present up front.
	var out []string
	start := -1
	if len(s) >= 1 && (s[0] == 'A' || s[0] == 'a') && len(s) > 1 && strings.ContainsRune(".、．)）", rune(s[1])) {
		start = regexp.MustCompile(`^A[\.、．)）]\s*`).FindStringIndex(s)[1]
	} else {
		return nil
	}
	for _, loc := range idxs {
		out = append(out, strings.TrimSpace(s[start:loc[0]]))
		start = loc[1]
	}
	if start >= 0 && start < len(s) {
		out = append(out, strings.TrimSpace(s[start:]))
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// GenerateAll runs Generate for every entry in the spec table.
func (g *Generator) GenerateAll(ctx context.Context, spec models.SpecTable) ([]models.GeneratedQuestion, error) {
	if len(spec.Entries) == 0 {
		return nil, fmt.Errorf("spec table 没有题位")
	}
	seen := make(map[string]struct{}, len(spec.Entries))
	for _, entry := range spec.Entries {
		if _, exists := seen[entry.ID]; exists {
			return nil, fmt.Errorf("spec table 包含重复题位 ID %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		if issues := quality.SubjectContentIssues(spec.Subject, entry.Points, ""); len(issues) > 0 {
			return nil, fmt.Errorf("spec %s 超出科目考纲：%s", entry.ID, strings.Join(issues, "；"))
		}
	}
	if isEnglishGroupedSubject(spec.Subject) {
		return g.generateEnglishAll(ctx, spec)
	}
	qs := make([]models.GeneratedQuestion, 0, len(spec.Entries))
	for i, entry := range spec.Entries {
		q, err := g.GenerateForSubject(ctx, spec.Subject, entry)
		if err != nil {
			return nil, fmt.Errorf("generate spec %s: %w", entry.ID, err)
		}
		fmt.Printf("[generate %d/%d] %s %.0f分 题面%d字\n", i+1, len(spec.Entries), entry.Type, entry.Score, len([]rune(q.Stem)))
		qs = append(qs, q)
	}
	return qs, nil
}

func isEnglish2Subject(subject string) bool {
	subject = strings.ToLower(strings.TrimSpace(subject))
	return subject == "english2" || subject == "英语二"
}

func isEnglish1Subject(subject string) bool {
	subject = strings.ToLower(strings.TrimSpace(subject))
	return subject == "english1" || subject == "英语一"
}

// isEnglishGroupedSubject reports whether the paper uses the fixed composite
// layout whose questions share passages and option lists.
func isEnglishGroupedSubject(subject string) bool {
	return isEnglish1Subject(subject) || isEnglish2Subject(subject)
}

// englishSubjectTag normalises a subject to the tag used in knowledge-point
// paths ("english1"/"english2").
func englishSubjectTag(subject string) string {
	if isEnglish1Subject(subject) {
		return "english1"
	}
	return "english2"
}

// englishLabel names the paper in prompts and errors.
func englishLabel(subject string) string {
	if isEnglish1Subject(subject) {
		return "英语一"
	}
	return "英语二"
}

// validateEnglishSpec keeps the official section map authoritative: every
// position must exist exactly once, carry the official type, score and section
// path, and the fixed-count subjects must not be short of positions.
func validateEnglishSpec(subject string, entries []models.SpecEntry) (map[int]models.SpecEntry, error) {
	label := englishLabel(subject)
	size := models.EnglishPaperSize(subject)
	if size == 0 {
		return nil, fmt.Errorf("不支持的英语科目 %q", subject)
	}
	if len(entries) != size {
		return nil, fmt.Errorf("%s按官方结构必须有 %d 个题位，当前 %d", label, size, len(entries))
	}
	byPosition := make(map[int]models.SpecEntry, len(entries))
	for _, entry := range entries {
		position, ok := models.EnglishQuestionPosition(entry.Points)
		if !ok {
			return nil, fmt.Errorf("spec %s 缺少有效的%s题位", entry.ID, label)
		}
		slot, ok := models.EnglishSlotFor(subject, position)
		if !ok {
			return nil, fmt.Errorf("spec %s 的题位 %d 超出%s结构", entry.ID, position, label)
		}
		if _, exists := byPosition[position]; exists {
			return nil, fmt.Errorf("%s题位 %d 重复", label, position)
		}
		want, ok := models.EnglishPointsPath(subject, position)
		if !ok {
			return nil, fmt.Errorf("spec %s 的题位 %d 超出%s结构", entry.ID, position, label)
		}
		if issue := englishPointPathIssue(entry.Points, want, slot, position, label); issue != "" {
			return nil, errors.New(issue)
		}
		if entry.Type != slot.Type || math.Abs(entry.Score-slot.Score) > 0.01 {
			return nil, fmt.Errorf("%s题位 %d 的题型/分值错误：%s %.2f，要求 %s %.2f", label, position, entry.Type, entry.Score, slot.Type, slot.Score)
		}
		byPosition[position] = entry
	}
	for position := 1; position <= size; position++ {
		if _, exists := byPosition[position]; !exists {
			return nil, fmt.Errorf("%s缺少题位 %d", label, position)
		}
	}
	return byPosition, nil
}

// englishPointPathIssue reports how a slot's knowledge-point path deviates from
// the official map for that position.
func englishPointPathIssue(points, want []string, slot models.EnglishSlot, position int, label string) string {
	if len(points) < 2 {
		return fmt.Sprintf("%s题位 %d 缺少分区知识点", label, position)
	}
	switch slot.Kind {
	case models.EnglishCloze:
		return ""
	case models.EnglishPartA:
		if strings.TrimSpace(points[1]) != "阅读理解" || len(points) < 4 || !strings.EqualFold(strings.TrimSpace(points[2]), "Part A") || strings.TrimSpace(points[3]) != want[3] {
			return fmt.Sprintf("%s题位 %d 必须属于阅读 Part A %s", label, position, want[3])
		}
	case models.EnglishPartB:
		if !models.IsEnglishPartB(points) || len(points) < 4 || !models.IsEnglishPartBTaskLabel(subjectOfPoints(points), points[3]) {
			return fmt.Sprintf("%s题位 %d 必须属于阅读 Part B（%s）", label, position, want[3])
		}
	case models.EnglishTranslation:
		if strings.TrimSpace(points[1]) != "翻译" {
			return fmt.Sprintf("%s题位 %d 必须是 Part C 英译汉", label, position)
		}
	case models.EnglishWritingA:
		if len(points) < 3 || strings.TrimSpace(points[1]) != "写作" || !strings.Contains(strings.TrimSpace(points[2]), "应用文") {
			return fmt.Sprintf("%s题位 %d 必须是写作 Part A 应用文", label, position)
		}
	default:
		if len(points) < 3 || strings.TrimSpace(points[1]) != "写作" {
			return fmt.Sprintf("%s题位 %d 必须是写作 Part B 作文", label, position)
		}
		if slot.Score >= 15 && !strings.Contains(strings.TrimSpace(points[2]), "图表") && !strings.Contains(strings.TrimSpace(points[2]), "主题") {
			return fmt.Sprintf("%s题位 %d 必须是写作 Part B 图表/主题作文", label, position)
		}
	}
	return ""
}

// subjectOfPoints recovers the subject tag from a knowledge-point path.
func subjectOfPoints(points []string) string {
	if len(points) == 0 {
		return ""
	}
	return points[0]
}

// validateEnglish2Spec is kept for the English II tests and callers.
func validateEnglish2Spec(entries []models.SpecEntry) (map[int]models.SpecEntry, error) {
	return validateEnglishSpec("english2", entries)
}

func englishRange(byPosition map[int]models.SpecEntry, start, end int) []models.SpecEntry {
	entries := make([]models.SpecEntry, 0, end-start+1)
	for position := start; position <= end; position++ {
		entries = append(entries, byPosition[position])
	}
	return entries
}

// generateEnglishAll builds a fixed-layout English paper group by group: the
// cloze questions share one passage, each Part A text shares one passage across
// its five questions, Part B shares one option list, and English I Part C shares
// one passage across its five translation items.
func (g *Generator) generateEnglishAll(ctx context.Context, spec models.SpecTable) ([]models.GeneratedQuestion, error) {
	// Sections are generated one after another, each in its own request: remember
	// the passages already used so the model picks different topics.
	g.ResetArticleHistory()
	tag := englishSubjectTag(spec.Subject)
	byPosition, err := validateEnglishSpec(spec.Subject, spec.Entries)
	if err != nil {
		return nil, err
	}
	groups := englishGroups(tag, byPosition)
	total := len(spec.Entries)
	generated := make(map[string]models.GeneratedQuestion, total)
	completed := 0
	if g.LLM == nil {
		// Offline mode: mirror GenerateForSubject's placeholder fallback so the
		// pipeline still flows without a model client.
		for _, group := range groups {
			for _, question := range englishPlaceholders(tag, group.id, group.entries) {
				generated[question.SpecID] = question
			}
		}
	} else {
		for _, group := range groups {
			questions, groupErr := g.generateEnglishGroupWithArticle(ctx, spec.Subject, group.id, group.entries, "")
			if groupErr != nil {
				// Same contract as the single-question path: a failed group degrades
				// to placeholders instead of aborting the whole paper. The validator
				// and repair loop then surface and retry the missing content.
				fmt.Printf("[generate] %s %s 题组降级为占位内容: %v\n", englishLabel(spec.Subject), group.id, groupErr)
				questions = englishPlaceholders(tag, group.id, group.entries)
			}
			for _, question := range questions {
				generated[question.SpecID] = question
				completed++
				fmt.Printf("[generate %d/%d] %s %.1f分 题面%d字\n", completed, total, question.Type, question.Score, len([]rune(question.Stem)))
			}
		}
	}
	for _, position := range englishSingletons(tag) {
		entry := byPosition[position]
		question, genErr := g.GenerateForSubject(ctx, spec.Subject, entry)
		if genErr != nil {
			return nil, fmt.Errorf("generate spec %s: %w", entry.ID, genErr)
		}
		generated[question.SpecID] = question
		completed++
		fmt.Printf("[generate %d/%d] %s %.1f分 题面%d字\n", completed, total, question.Type, question.Score, len([]rune(question.Stem)))
	}
	return orderEnglishQuestions(spec, generated)
}

// RegenerateEnglishGroup rebuilds one shared-material group during paper repair.
// Regenerating a single slot of a composite group would drop the shared passage
// and break the composite contract, so callers repair whole groups.
// articleText lets a repair reuse the passage already stored on the paper; pass
// "" to acquire a fresh one.
func (g *Generator) RegenerateEnglishGroup(ctx context.Context, subject, groupID string, entries []models.SpecEntry, articleText string) ([]models.GeneratedQuestion, error) {
	if g.LLM == nil {
		return englishPlaceholders(englishSubjectTag(subject), groupID, entries), nil
	}
	return g.generateEnglishGroupWithArticle(ctx, subject, groupID, entries, articleText)
}

// englishGroupSpec is one composite group: a shared passage (and option list)
// plus the slots that reference it.
type englishGroupSpec struct {
	id      string
	entries []models.SpecEntry
}

// englishGroups lists the composite groups of a paper in generation order.
func englishGroups(tag string, byPosition map[int]models.SpecEntry) []englishGroupSpec {
	groups := []englishGroupSpec{
		{id: tag + "-cloze", entries: englishRange(byPosition, 1, 20)},
		{id: tag + "-reading-1", entries: englishRange(byPosition, 21, 25)},
		{id: tag + "-reading-2", entries: englishRange(byPosition, 26, 30)},
		{id: tag + "-reading-3", entries: englishRange(byPosition, 31, 35)},
		{id: tag + "-reading-4", entries: englishRange(byPosition, 36, 40)},
		{id: tag + "-part-b", entries: englishRange(byPosition, 41, 45)},
	}
	if tag == "english1" {
		// English I Part C is one passage carrying five translation items.
		groups = append(groups, englishGroupSpec{id: tag + "-translation", entries: englishRange(byPosition, 46, 50)})
	}
	return groups
}

// englishSingletons lists the positions generated as standalone questions.
func englishSingletons(tag string) []int {
	if tag == "english1" {
		return []int{51, 52}
	}
	return []int{46, 47, 48}
}

// englishGroupKind reports which shared-material shape a group id describes.
func englishGroupKind(groupID string) string {
	switch {
	case strings.HasSuffix(groupID, "-cloze"):
		return "cloze"
	case strings.HasSuffix(groupID, "-part-b"):
		return "part-b"
	case strings.HasSuffix(groupID, "-translation"):
		return "translation"
	default:
		return "reading"
	}
}

// englishPlaceholders builds offline stand-ins for one composite group so the
// pipeline can flow without a model client. They stay deliberately minimal:
// Status "needs_review" plus a warning marks them as content to regenerate.
func englishPlaceholders(tag, groupID string, entries []models.SpecEntry) []models.GeneratedQuestion {
	questions := make([]models.GeneratedQuestion, 0, len(entries))
	label := "英语二"
	if tag == "english1" {
		label = "英语一"
	}
	material := fmt.Sprintf("%% TODO: %s题组共享材料（LLM 不可用）", label)
	for _, entry := range entries {
		position, _ := models.EnglishQuestionPosition(entry.Points)
		questions = append(questions, models.GeneratedQuestion{
			ID:             fmt.Sprintf("gen_%s", entry.ID),
			SpecID:         entry.ID,
			Type:           entry.Type,
			Points:         entry.Points,
			Score:          entry.Score,
			Status:         "needs_review",
			GroupID:        groupID,
			SharedMaterial: material,
			Stem:           fmt.Sprintf("%% TODO: %s题位 %d 题面（LLM 不可用）", label, position),
			Answer:         "% TODO: 参考答案",
			Difficulty:     bandToFloat(entry.DiffBand),
			Warnings:       []string{"LLM不可用，题面和答案是占位内容"},
		})
	}
	return questions
}

// english2Placeholders is kept for callers that still use the English II name.
func english2Placeholders(groupID string, entries []models.SpecEntry) []models.GeneratedQuestion {
	return englishPlaceholders("english2", groupID, entries)
}

func orderEnglishQuestions(spec models.SpecTable, generated map[string]models.GeneratedQuestion) ([]models.GeneratedQuestion, error) {
	ordered := make([]models.GeneratedQuestion, 0, len(spec.Entries))
	for _, entry := range spec.Entries {
		question, exists := generated[entry.ID]
		if !exists {
			return nil, fmt.Errorf("英语题位 %s 未生成", entry.ID)
		}
		ordered = append(ordered, question)
	}
	return ordered, nil
}

// englishWeakItemPrompt lists the failing items with their current answers so the
// model can rewrite exactly those.
func englishWeakItemPrompt(entries []models.SpecEntry, response english2GroupResponse, weak map[string]string) string {
	current := make(map[string]english2GroupItem, len(response.Items))
	for _, item := range response.Items {
		current[item.SpecID] = item
	}
	var b strings.Builder
	for _, entry := range entries {
		issue, failing := weak[entry.ID]
		if !failing {
			continue
		}
		item := current[entry.ID]
		fmt.Fprintf(&b, "- %s（%s）当前 answer：%s\n  不合格原因：%s\n", entry.ID, entry.Points[len(entry.Points)-1], item.Answer, issue)
	}
	return b.String()
}

// mergeEnglishGroupItems copies the rewritten items over the current ones and
// keeps the rest of the group untouched.
func mergeEnglishGroupItems(response *english2GroupResponse, rewrite english2GroupResponse) {
	updates := make(map[string]english2GroupItem, len(rewrite.Items))
	for _, item := range rewrite.Items {
		updates[item.SpecID] = item
	}
	for i := range response.Items {
		item := &response.Items[i]
		updated, ok := updates[item.SpecID]
		if !ok {
			continue
		}
		if strings.TrimSpace(updated.Answer) != "" {
			item.Answer = updated.Answer
		}
		if strings.TrimSpace(updated.Stem) != "" {
			item.Stem = updated.Stem
		}
		if len(updated.Options) > 0 {
			item.Options = updated.Options
		}
	}
}

// englishGroupAnswerRequirement restates the answer shape the quality gate
// enforces, so a rewritten item can actually pass it.
func englishGroupAnswerRequirement(kind string) string {
	switch kind {
	case "part-b":
		return "answer 以“答案：X。”开头（X 为 A-G 中一个字母），随后用中文写明语法衔接、指代或逻辑依据，并说明其余备选句为何不符，必须出现“上下文/语境/衔接/逻辑/不符/排除”等说明性表述；英文原词要用引号或括号标注。"
	case "translation":
		return "answer 以“答案：”开头给出该段的中文译文，再补充翻译要点（长难句结构、指代、词义选择等），不少于 30 个汉字。"
	default:
		return "answer 以“答案：X。”开头（X 为 A-D 中一个字母），随后逐项说明四个选项：分别以“ A项：”“B项：”“C项：”“D项：”开头，写明正确项的上下文/语法/搭配依据，并指出其余选项为何不符。"
	}
}

// englishSubjectChineseNumber renders the exam's Chinese ordinal for prompts.
func englishSubjectChineseNumber(tag string) string {
	if tag == "english1" {
		return "一"
	}
	return "二"
}

// englishGroupInstructions returns the prompt header and the group-specific
// authoring rules for one group.
func englishGroupInstructions(tag, kind, groupID string) (groupType, rules string) {
	switch kind {
	case "cloze":
		return "英语知识运用（完形填空）", fmt.Sprintf("生成一篇完整、原创、连贯且至少 250 个英文词的完形短文。最重要的硬约束：sharedMaterial 正文中必须实际出现恰好 20 个编号空，字面值依次为 [1]、[2]、[3]……[20]，每个编号各出现一次，不得改成下划线、圆括号、尖括号、空格或其他记号，也不得出现 [21] 等其他编号。请在返回 JSON 前逐个搜索并核对 [1] 至 [20]。每个 item stem 必须准确写为对应的 Blank [n]，不得重写文章；每空提供 4 个词/短语选项。解析须说明正确项的上下文、语法或搭配依据，并逐项排除其余 A-D 选项。不得拆成 20 篇短文；sharedMaterial 只放一篇完整文章。")
	case "part-b":
		if tag == "english1" {
			return "阅读理解 Part B 七选五（选择句子填空）", "严格仿照英语一 Part B 的七选五题型：sharedMaterial 是一篇至少 250 个英文词的完整英文材料，正文中必须实际出现恰好 5 个编号空，字面值依次为 [41]、[42]、[43]、[44]、[45]，每个编号各出现一次。sharedOptions 提供 7 条互不重复的完整英文句子（每条 8-20 词，能从语法与语义上填入某个编号空位），不要在句子前加 A-G 字母（排版会自动加），其中恰有 2 条干扰项。items 对应第 41-45 题，stem 必须准确写为对应的 Blank [n]（n 为该题真实题位号），不得重写文章；每题 options 必须为空，answer 是 A-G 中唯一一个字母，五题答案不得重复。每题用不少于 30 个汉字解释该空与所选句子的语法衔接、指代或逻辑依据，并说明干扰项为何不合适。不得改成普通四选一、人物观点匹配、段落排序或小标题对应。"
		}
		return "阅读理解 Part B 人物观点匹配", "严格仿照 2026 年英语二 Part B 人物观点匹配题：sharedMaterial 是一篇至少 250 个英文词的完整英文材料，其中必须清晰介绍或引用 5 位不同的具名人物。sharedOptions 提供 7 条互不重复的英文观点陈述，不要在陈述文本前加 A-G 字母（排版会自动加），其中恰有 2 条干扰项。items 对应第 41-45 题，stem 必须依次填写 5 位人物的完整英文姓名，不能写 Paragraph 1-5、题号、泛称或中文标签；每个姓名必须在 sharedMaterial 中原样出现且五题不得重复。每题 options 必须为空，answer 是 A-G 中唯一一个字母，五题答案不得重复。每题用不少于 30 个汉字解释该人物发言或观点与对应陈述的依据。不得改成普通四选一、段落小标题匹配、段落排序或七选五。"
	case "translation":
		return "翻译 Part C（英译汉，第 46-50 题）", "严格仿照英语一 Part C：sharedMaterial 是一篇 150-220 个英文词、结构完整、有内在逻辑的英文短文（可来自科普、社会、文化等题材）。items 对应第 46-50 题，每题 stem 必须原样抄写 sharedMaterial 中连续的一段或一句完整英文（5 题合起来应覆盖短文的主要句子，且每段都必须是 sharedMaterial 的子串），不得改写或另造句子。每题 options 必须为空；answer 必须以“答案：”开头给出该段的中文译文，再补充不少于 30 个汉字的翻译要点说明（如长难句拆解、指代、被动语态、词义选择等）。译文须通顺、准确，不得逐词硬译。"
	default:
		return "阅读理解 Part A 共文题组", "生成一篇完整、原创、连贯且至少 250 个英文词的阅读文章，并针对该文生成 5 道相互关联但考点有区分的四选一题。五题必须全部依据同一篇 sharedMaterial 作答，不得为单题另写文章或在题干重复文章。考查细节、推断、主旨、词义/指代、作者态度等不同能力。每题提供 4 个选项；解析须说明正确项依据并逐项排除其余 A-D 选项。"
	}
}

// englishGroupAnswerContract describes the required answer shape for the group.
func englishGroupAnswerContract(kind string) string {
	switch kind {
	case "part-b":
		return "Part B 时每题 options 必须为空，七个备选句/观点只放 sharedOptions；每题 stem 必须是 Blank [n] 或人物姓名，answer 只写一个 A-G 字母加中文解析。中文解析里引用英文原词、短语或句子时必须用引号或括号标注（例如 “quiet revolution”），不得把英文单词直接夹在中文句子里当作中文使用。"
	case "translation":
		return "翻译题每题 options 必须为空，answer 以“答案：”开头给出中文译文，再给出翻译要点说明。"
	default:
		return "客观题的 answer 必须以“答案：A/B/C/D”开头，并逐项解释四个选项；每一项前必须使用可识别的标签“ A项：”“B项：”“C项：”“D项：”或“Option A:”“Option B:”“Option C:”“Option D:”，不能只写“其余选项”。中文解析里引用英文原词、短语或句子时必须用引号或括号标注，不得把英文单词直接夹在中文句子里当作中文使用。"
	}
}

// englishGroupRetryHint repeats the hardest constraint when a group is redone.
func englishGroupRetryHint(kind string) string {
	switch kind {
	case "cloze":
		return "对于完形题，sharedMaterial 必须逐字包含 [1] [2] [3] …… [20] 这 20 个编号空；如果上一版用了下划线或其他符号，必须全部改成方括号编号。返回前逐个核对每个编号恰好出现一次。"
	case "part-b":
		return "Part B 的编号空必须写在 sharedMaterial 里（依次为 [41] 至 [45] 各一次），每题 stem 只写对应的 Blank [n]；备选句只放 sharedOptions。每题解析必须写明语法衔接、指代或逻辑依据，并说明其余备选句为何不符（要出现“上下文/语境/衔接/逻辑/不符/排除”等说明性表述），不能只写“答案：X”。"
	case "translation":
		return "翻译题的每题 stem 必须是 sharedMaterial 中的原文片段（逐字子串），不得改写；译文放 answer，并附翻译要点（长难句结构、指代、词义选择等）。"
	default:
		return "五道题必须全部依据同一篇 sharedMaterial 作答，不得重复携带文章全文。每题解析必须逐项说明四个选项：分别以“ A项：”“B项：”“C项：”“D项：”开头，缺一项即视为不合格；同时写明正确选项的上下文/语法依据。"
	}
}

// validateEnglishGroupResponse checks one generated group against its section
// contract before anything is persisted: the shared material must be long
// enough and carry the numbered blanks the section needs, every slot must be
// answered once, and Part B answers must use distinct shared options.
func validateEnglishGroupResponse(tag, groupID string, entries []models.SpecEntry, response english2GroupResponse) string {
	kind := englishGroupKind(groupID)
	if strings.TrimSpace(response.SharedMaterial) == "" {
		return "缺少 sharedMaterial"
	}
	if wordCount := len(englishGroupWordPattern.FindAllString(response.SharedMaterial, -1)); wordCount < 180 {
		return fmt.Sprintf("共用材料仅 %d 个英文词，至少需要 180 词", wordCount)
	}
	if len(response.Items) != len(entries) {
		return fmt.Sprintf("题目数量为 %d，要求 %d", len(response.Items), len(entries))
	}
	wanted := make(map[string]bool, len(entries))
	positions := make(map[string]int, len(entries))
	for _, entry := range entries {
		wanted[entry.ID] = true
		if position, ok := models.EnglishQuestionPosition(entry.Points); ok {
			positions[entry.ID] = position
		}
	}
	switch kind {
	case "cloze":
		first, last := 1, 20
		if issue := checkNumberedBlanks(response.SharedMaterial, first, last); issue != "" {
			return issue
		}
	case "part-b":
		if len(response.SharedOptions) != 7 {
			return fmt.Sprintf("Part B 备选项数量为 %d，要求 7", len(response.SharedOptions))
		}
		if tag == "english1" {
			if issue := checkNumberedBlanks(response.SharedMaterial, 41, 45); issue != "" {
				return issue
			}
		}
	}
	seen := make(map[string]bool, len(response.Items))
	partBAnswers := make(map[string]bool, 5)
	for _, item := range response.Items {
		if !wanted[item.SpecID] || seen[item.SpecID] {
			return "specId 未对应题位或重复"
		}
		seen[item.SpecID] = true
		if strings.TrimSpace(item.Stem) == "" || strings.TrimSpace(item.Answer) == "" {
			return "存在空题干或空答案"
		}
		switch kind {
		case "part-b":
			if len(item.Options) != 0 {
				return "Part B 单题选项必须留空，备选句/观点只能作为共享选项出现一次"
			}
			answer := choice.ParseWithAlphabet(item.Answer, "ABCDEFG")
			if issue := choice.ValidateWithAlphabet(answer, false, "ABCDEFG"); issue != "" {
				return fmt.Sprintf("Part B 题位 %s 答案无效：%s", item.SpecID, issue)
			}
			if partBAnswers[answer[0]] {
				return "Part B 五题答案必须一一匹配，不得重复使用同一备选项"
			}
			partBAnswers[answer[0]] = true
			if tag == "english1" {
				if !strings.Contains(item.Stem, fmt.Sprintf("[%d]", positions[item.SpecID])) {
					return fmt.Sprintf("Part B 题位 %s 的题干必须准确引用对应的编号空", item.SpecID)
				}
				continue
			}
			target := strings.TrimSpace(item.Stem)
			if !partBPersonNamePattern.MatchString(target) {
				return fmt.Sprintf("Part B 题位 %s 必须填写一位完整英文姓名，不能使用 Paragraph、题号或其他题型", item.SpecID)
			}
			if !strings.Contains(strings.ToLower(response.SharedMaterial), strings.ToLower(target)) {
				return fmt.Sprintf("Part B 题位 %s 的人物姓名未出现在 sharedMaterial 中", item.SpecID)
			}
		case "translation":
			if len(item.Options) != 0 {
				return "翻译题每题选项必须为空"
			}
			stem := strings.TrimSpace(item.Stem)
			if !strings.Contains(response.SharedMaterial, stem) {
				return fmt.Sprintf("翻译题位 %s 的题干必须是 sharedMaterial 中的原文片段", item.SpecID)
			}
		default:
			if len(item.Options) != 4 {
				return fmt.Sprintf("题位 %s 的四选项数量为 %d", item.SpecID, len(item.Options))
			}
			if kind == "cloze" && !strings.Contains(item.Stem, fmt.Sprintf("[%d]", positions[item.SpecID])) {
				return fmt.Sprintf("完形题位 %s 的题干必须准确引用对应的编号空", item.SpecID)
			}
		}
	}
	if kind == "part-b" {
		seenOptions := make(map[string]bool, 7)
		for _, option := range response.SharedOptions {
			value := strings.TrimSpace(option)
			if value == "" || seenOptions[value] || partBStatementPrefixPattern.MatchString(value) {
				return "Part B 备选项为空、重复或已带 A-G 标签"
			}
			seenOptions[value] = true
		}
	}
	return ""
}

// checkNumberedBlanks verifies that each [first]…[last] marker appears exactly
// once in the shared material and that no other numbered blank is present.
func checkNumberedBlanks(material string, first, last int) string {
	markers := make(map[int]int, last-first+1)
	for _, match := range englishClozeMarkerPattern.FindAllStringSubmatch(material, -1) {
		var number int
		if _, err := fmt.Sscanf(match[1], "%d", &number); err != nil || number < first || number > last {
			return fmt.Sprintf("共用材料只能包含 [%d] 至 [%d] 编号空", first, last)
		}
		markers[number]++
	}
	for number := first; number <= last; number++ {
		if markers[number] != 1 {
			return fmt.Sprintf("共用材料中的 [%d] 空位出现 %d 次，要求恰好一次", number, markers[number])
		}
	}
	return ""
}

// validateEnglish2GroupResponse is kept for the English II tests.
func validateEnglish2GroupResponse(groupID string, entries []models.SpecEntry, response english2GroupResponse) string {
	return validateEnglishGroupResponse("english2", groupID, entries, response)
}

// buildSystemPrompt constructs the system prompt for question generation.
func (g *Generator) buildSystemPrompt(spec models.SpecEntry) string {
	return g.buildSystemPromptForSubject("", spec)
}

func (g *Generator) buildSystemPromptForSubject(subject string, spec models.SpecEntry) string {
	typeDesc := typeDescription(spec.Type)
	cogDesc := cognitiveDescription(spec.Cognitive)
	diffDesc := difficultyDescription(spec.DiffBand)

	answerRule := "单选题只能有一个正确选项"
	if spec.Type == models.TypeMultiChoice {
		answerRule = "多选题必须有至少两个正确选项，答案可写成 A,C 或 AC"
	}
	typeRule := questionFormatRule(spec.Type)
	subjectRule := subjectQuestionRule(subject, spec)
	// English papers are written article-first: the passage is produced (or
	// fetched) before any question exists, so the prompt describes the section
	// the item belongs to instead of a knowledge point that would drive the model
	// to invent an unrelated topic.
	specLine := fmt.Sprintf("知识点: %s", strings.Join(spec.Points, ", "))
	if models.EnglishPaperSize(subject) == 0 && !models.IsEnglishSubject(subject) {
		// keep the knowledge-point line for the corpus-composed subjects
	} else {
		specLine = fmt.Sprintf("试卷部分: %s", englishSectionLabel(subject, spec))
	}
	return fmt.Sprintf(`你是一个考研命题专家。你需要根据以下规格生成一道完整、可独立作答且经过验算的考研模拟题:

题型: %s
%s
认知层次(Bloom): %s
难度目标: %s (band %d，数值必须落在该区间)
分值: %.2f

要求:
1. 题面必须用 LaTeX 格式输出, 不含 \documentclass 或 \begin{document}
2. 题面内容必须严格围绕给定知识点, 不得偏题
3. 难度必须匹配目标难度band (%s)
4. 认知层次必须匹配 (%s) — 不要用记忆题代替应用题
5. 选择题必须提供4个互不重复且非空的选项 (A/B/C/D); %s
6. 必须严格使用题型对应格式：%s
7. 参考答案必须包含“答案/结果”和可供考生复现的清晰解析，禁止只返回选项、数字、结论或“显然/易知”等跳步表述。选择题的 stem 只写题干，不得把 A-D 选项再次写入 stem；选项仅放在 options 字段。解析按“考查目标与判据→逐步推导→对照选项→结论”组织：说明每一步使用的条件/定理，展示关键中间结果，再把推导结果与选项原文核对；不得只重复答案。多选题必须分别分析 A、B、C、D 并说明判断依据。最终答案必须和解析推导出的选项逐字符核对，尤其是序列、数值、公式等内容，禁止出现推导得出一个选项却标注另一个字母的矛盾。填空题给出公式、代入、化简和结果检验；解答题按步骤写出方法、关键等式/中间结果，并解释结论如何由上一步推出。英语选择题须结合上下文线索、语法/搭配或词义辨析，并逐项说明正确项与干扰项为何合适/不合适；英语翻译/写作题给出完整参考文本，并分点说明信息覆盖、逻辑组织和语言表达等评分依据。返回前独立复核题面条件、计算、选项和最终结论；选择题答案首句明确写“答案：A/B/C/D”；不要重复题干，不要输出无关说明
8. answer 字段的书写顺序必须是：解析在前，最后一行另起一行写“最终答案：<与解析结论完全一致的最终结论>”。不要先写答案摘要再推导：推导尚未完成时写下的结论经常与解析矛盾。首行若已有“答案：…”，其内容必须与末尾“最终答案：…”逐字符一致（数值、符号、分支、单位都要一致，多分支题必须写全（如 ±、或用文字说明各分支取值））
9. 题目必须自洽且信息足以确定答案；禁止在答案中写“题目有误”“假设题意”“修改题面”“重新审视”“如果题目要求”等自我纠错或不确定性说明。若草稿发现条件矛盾，直接重新设计一道自洽的新题后再输出
10. 难度数值必须服从区间：易为 [0.00,0.34]，中为 [0.35,0.64]，难为 [0.65,1.00]；不要把所有题都填写为 0.5
11. 以JSON格式返回且只返回这4个字段: {"stem": "LaTeX题面", "options": ["选项A", "选项B", "选项C", "选项D"], "answer": "答案与解析", "difficulty": 0.75}
12. %s`,
		typeDesc,
		specLine,
		cogDesc,
		diffDesc,
		spec.DiffBand,
		spec.Score,
		diffDesc,
		cogDesc,
		answerRule,
		typeRule,
		subjectRule,
	)
}

// englishSectionLabel names the section an English slot belongs to, e.g.
// "阅读理解 Part B（新题型）". English prompts use it instead of a knowledge-point
// list: the section's article is written first and the item is derived from it.
func englishSectionLabel(subject string, spec models.SpecEntry) string {
	position, ok := models.EnglishQuestionPosition(spec.Points)
	if !ok {
		return strings.Join(spec.Points, ", ")
	}
	slot, ok := models.EnglishSlotFor(subject, position)
	if !ok {
		return strings.Join(spec.Points, ", ")
	}
	label := englishLabel(subject)
	switch slot.Kind {
	case models.EnglishCloze:
		return fmt.Sprintf("%s 英语知识运用（完形填空）第 %d 空", label, position)
	case models.EnglishPartA:
		return fmt.Sprintf("%s 阅读理解 Part A Text %d 第 %d 题", label, slot.Text, position)
	case models.EnglishPartB:
		return fmt.Sprintf("%s 阅读理解 Part B 新题型 第 %d 题", label, position)
	case models.EnglishTranslation:
		return fmt.Sprintf("%s 翻译 Part C 第 %d 题", label, position)
	case models.EnglishWritingA:
		return fmt.Sprintf("%s 写作 Part A 应用文（第 %d 题）", label, position)
	default:
		return fmt.Sprintf("%s 写作 Part B 作文（第 %d 题）", label, position)
	}
}

func englishQuestionRule(spec models.SpecEntry) string {
	english := false
	for _, point := range spec.Points {
		normalized := strings.ToLower(strings.TrimSpace(point))
		if strings.Contains(normalized, "英语一") || strings.Contains(normalized, "英语二") ||
			strings.Contains(normalized, "english1") || strings.Contains(normalized, "english2") {
			english = true
			break
		}
	}
	if !english {
		return "无额外科目格式要求"
	}
	if models.IsEnglishPartB(spec.Points) {
		return "英语二 Part B 是复合人物观点匹配题：5 位具名人物共用 A-G 七条观点陈述，包含 2 个干扰项；题位 41-45 必须依次对应 sharedMaterial 中 5 位不同人物，不得改成普通四选一或段落小标题匹配题。答案允许 A-G，题组共享材料由生成器提供。"
	}
	if len(spec.Points) > 2 && strings.EqualFold(strings.TrimSpace(spec.Points[0]), "english2") {
		switch strings.TrimSpace(spec.Points[1]) {
		case "翻译":
			return "英语二 Part C 必须给出一整段原创英文材料（约 130-160 词）供完整英译汉，不得只给孤立句子；参考答案给出完整、通顺的中文译文及要点说明。"
		case "写作":
			if strings.Contains(spec.Points[2], "应用文") {
				return "英语二写作 Part A 必须是明确的应用文任务（如邮件/书信/通知），给出具体情境和需要覆盖的要点，要求约 100 词；参考答案使用规范格式并覆盖全部要点。"
			}
			return "英语二写作 Part B 必须提供可直接读取的图表或表格数据（至少 3 个类别及明确数值/比例），不能只写‘见图’或依赖未附带图片；要求描述并阐释数据、给出评论，约 150 词。"
		}
	}
	switch spec.Type {
	case models.TypeChoice:
		return "英语客观题必须是自然、完整、可独立作答的真题风格题目；题面必须原创，不得照抄或近似改写 few-shot、历年真题或其专有句子；若考查单个完形/词汇空，全文只能留一个待答空，不能出现其他未提供答案选项的空，也不能称为‘第二空’；不得加入介绍题目构造的元叙述（如‘The passage contains a context...’）。英文题干和选项表达地道，答案首行写‘答案：X（正确词/短语）’，随后用清晰中文说明上下文线索、语法搭配、词义辨析及其余选项不当之处。"
	case models.TypeMajor:
		return "英语主观题须依据知识点生成完整翻译、写作或阅读任务；题面材料和任务要求齐全，参考答案给出完整译文/范文，并简要说明关键信息和语言依据，不得只给泛泛结论。"
	default:
		return "英语题须使用自然地道的英文材料，并提供完整、明确的作答要求与答案解析。"
	}
}

func subjectQuestionRule(subject string, spec models.SpecEntry) string {
	if subject == "politics" || subject == "政治" {
		return "本题属于考研思想政治理论，只能围绕题位给出的六个考纲模块和知识点命题：马克思主义基本原理、毛泽东思想和中国特色社会主义理论体系概论、习近平新时代中国特色社会主义思想概论、中国近现代史纲要、思想道德与法治、形势与政策以及当代世界经济与政治。必须生成政治理论题，严禁生成数学、概率统计、计算机或英语题；题干与参考答案均须紧扣给定知识点，不得用 choice_auto 等占位标签推测其他学科。选择题考查政治理论辨析或材料应用，选项保持同一概念层级；分析题须给出完整材料、明确设问和层次清楚的参考答案解析。涉及年度时政时只使用题位材料明确给出的事实，不得臆造最新政策、日期或数据。"
	}
	if subject == "math2" || subject == "数学二" {
		return "本题属于数学二。数学二不考无穷级数；禁止考查或引入常数项级数、幂级数、级数收敛/敛散性、收敛半径、傅里叶级数等内容。题位知识点若与此冲突，视为题位数据错误，不得按错误知识点出题。"
	}
	for _, point := range spec.Points {
		normalized := strings.ToLower(strings.TrimSpace(point))
		if normalized == "408" || strings.Contains(normalized, "计算机学科专业基础") {
			rule := "408 题必须给出足以复核的完整条件；图算法须明确有向/无向、起点、邻接访问次序及复杂度覆盖范围。DFS/BFS 题逐步跟踪访问序列、队列/递归栈及生成树；有向图环检测不能只凭 visited 布尔标志，须区分递归栈/颜色状态与已访问状态。解答题逐问作答并展示关键中间状态和复杂度推导，不得把‘阻止重复访问’误说成‘识别有向环’。"
			if strings.Contains(strings.Join(spec.Points, " "), "B+树") || strings.Contains(strings.Join(spec.Points, " "), "B树") {
				rule += "B树/B+树题必须明确阶数 m 定义为每个内部结点的最大子女数，故最多 m-1 个关键字；明确非根内部结点最少 ceil(m/2) 个子女、叶结点关键字上下限及分裂规则。仅当插入后关键字数超过 m-1 才分裂。B+树叶分裂时将有序关键字分为左右叶，右叶首关键字复制到父结点且仍保留在右叶；内部结点溢出时提升中位分隔键，并保持子女数=关键字数+1。答案须逐次列出溢出节点、提升/复制的键，给出有序且层次自洽的最终树和叶链。禁止将B树删除/提升规则套用于B+叶分裂，禁止把阶数说成最大关键字数。"
			}
			if strings.Contains(strings.Join(spec.Points, " "), "路由协议") || strings.Contains(strings.Join(spec.Points, " "), "RIP") {
				rule += "路由协议题若考 RIP，度量值只能按经过的路由器跳数计算，每条相邻链路增加 1；不得给链路另设 2 等任意开销。只能从拓扑中直接相邻的路由器接收距离向量，严禁让路由器收到非邻居的路由表。若题目按轮次推演，必须明确每个路由器各有一个直连局域网 N1、N2……，初始仅本地 LAN 的距离为 1，其余为无穷；采用同步轮次，每轮只依据上一轮已通告向量计算，经过 k 条路由器链路到达对端 LAN 的 RIP 距离为 k+1。每个问法须能由所给拓扑唯一推出，并在回答前逐轮验算。"
			}
			if spec.Type == models.TypeChoice {
				rule += "单选题必须先独立算出唯一结果，再核对四个选项；answer 首句与末句的答案字母必须相同，全文只能给出一个最终答案，推导中指出某选项正确后不得再标注其他字母。不得保留自我纠错、相互矛盾或修改答案的文字。"
			}
			return rule
		}
	}
	return englishQuestionRule(spec)
}

func questionFormatRule(questionType models.QuestionType) string {
	switch questionType {
	case models.TypeChoice:
		return "单项选择题；stem 必须是完整问句且明确要求选择，options 必须恰好有4项，answer 首句必须是‘答案：X’；禁止生成填空题、下划线空格或把答案留在题干中"
	case models.TypeMultiChoice:
		return "多项选择题；stem 必须是完整问句且明确要求选择，options 必须恰好有4项，answer 首句必须列出至少两个正确选项；禁止生成填空题"
	case models.TypeFillBlank:
		return "填空题；stem 必须明确留出填空位置，options 必须是空数组，answer 给出填空结果及必要计算"
	case models.TypeMajor:
		return "解答题；stem 给出完整条件和明确求解/证明目标，options 必须是空数组，answer 给出分步推导与结论"
	default:
		return "严格按指定题型生成"
	}
}

// buildUserPrompt constructs the user prompt with few-shot examples and template.
func (g *Generator) buildUserPrompt(spec models.SpecEntry) string {
	var b strings.Builder

	b.WriteString("请根据以上规格生成一道考研真题。\n\n")

	// Few-shot examples are filtered before they enter a prompt. A corrupted
	// parser artifact must never be copied into a new question.
	if examples, ok := cleanExamples(g.FewShotExamples[spec.TemplateKey]); ok {
		b.WriteString("以下是同类真题示例, 供参考风格和难度:\n\n")
		for i, ex := range examples {
			if i >= 3 { // limit to 3 few-shot examples
				break
			}
			b.WriteString(fmt.Sprintf("示例%d:\n%s\n\n", i+1, ex))
		}
	} else if examples, ok := cleanExamples(g.FewShotExamples[string(spec.Type)]); ok {
		b.WriteString("以下是同类真题示例, 供参考风格和难度:\n\n")
		for i, ex := range examples {
			if i >= 3 {
				break
			}
			b.WriteString(fmt.Sprintf("示例%d:\n%s\n\n", i+1, ex))
		}
	}

	b.WriteString("请生成题目, 返回JSON。")
	return b.String()
}

func cleanExamples(examples []string) ([]string, bool) {
	clean := make([]string, 0, len(examples))
	for _, example := range examples {
		text := strings.TrimSpace(example)
		if text == "" || quality.Polluted(text) || strings.Contains(strings.ToUpper(text), "TODO") {
			continue
		}
		clean = append(clean, text)
		if len(clean) == 3 {
			break
		}
	}
	return clean, len(clean) > 0
}

// selectModel picks the LLM model for a question type.
func (g *Generator) selectModel(t models.QuestionType) string {
	if m, ok := g.ModelByType[string(t)]; ok && m != "" {
		return m
	}
	return g.DefaultModel
}

// --- Helpers ---

func bandToFloat(b models.DifficultyBand) float64 {
	switch b {
	case models.BandEasy:
		return 0.25
	case models.BandMedium:
		return 0.5
	case models.BandHard:
		return 0.75
	default:
		return 0.5
	}
}

func typeDescription(t models.QuestionType) string {
	switch t {
	case models.TypeChoice:
		return "选择题 (单选, 4个选项)"
	case models.TypeMultiChoice:
		return "多项选择题 (至少两个正确选项)"
	case models.TypeFillBlank:
		return "填空题"
	case models.TypeMajor:
		return "大题 (计算/证明/算法设计)"
	default:
		return string(t)
	}
}

func cognitiveDescription(c models.CognitiveLevel) string {
	switch c {
	case models.CogRemember:
		return "记忆 (识记基本概念/定义)"
	case models.CogUnderstand:
		return "理解 (解释/比较/分类)"
	case models.CogApply:
		return "应用 (套用方法解决标准问题)"
	case models.CogAnalyze:
		return "分析 (分解/找关系/推断原因)"
	case models.CogEvaluate:
		return "评价 (判断/批判/标准检验)"
	case models.CogCreate:
		return "创造 (设计新方案/组合构建)"
	default:
		return string(c)
	}
}

func difficultyDescription(b models.DifficultyBand) string {
	switch b {
	case models.BandEasy:
		return "易 (多数考生能做对)"
	case models.BandMedium:
		return "中 (中等难度, 有一定区分度)"
	case models.BandHard:
		return "难 (高区分度, 少数考生能做对)"
	default:
		return "中"
	}
}
