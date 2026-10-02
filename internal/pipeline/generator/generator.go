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
	Stem       string   `json:"stem"`       // 题面 in LaTeX
	Options    []string `json:"options"`    // non-empty for choice questions
	Answer     string   `json:"answer"`     // 参考答案 in LaTeX
	Difficulty float64  `json:"difficulty"` // LLM self-estimated [0,1]
}

// Generate produces a single question from a spec entry.
//
// The prompt includes:
//   - the spec entry (type, points, cognitive, difficulty band, score)
//   - few-shot examples from real exams (if available)
//   - the LaTeX template skeleton (structural scaffold)
//   - instructions to produce stem + options + answer in LaTeX
func (g *Generator) Generate(ctx context.Context, spec models.SpecEntry) (models.GeneratedQuestion, error) {
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
	system := g.buildSystemPrompt(spec)

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
				return g.finish(spec, resp)
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

	question, err := g.finish(spec, resp)
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
		retried, finishErr := g.finish(spec, retryResp)
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
	q := models.GeneratedQuestion{
		ID:     fmt.Sprintf("gen_%s", spec.ID),
		SpecID: spec.ID,
		Type:   spec.Type,
		Points: spec.Points,
		Score:  spec.Score,
	}
	q.Stem = cleanGeneratedStem(resp.Stem)
	q.Options = normalizeOptions(resp.Options)
	q.Answer = resp.Answer
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
		if len(q.Options) != 4 {
			q.Status = "needs_review"
			q.Warnings = append(q.Warnings, fmt.Sprintf("选择题选项数量为 %d，期望 4", len(q.Options)))
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
			if issue := choice.Validate(labels, spec.Type == models.TypeMultiChoice); issue != "" {
				q.Status = "needs_review"
				q.Warnings = append(q.Warnings, "选择题参考答案无法解析为合法选项")
			}
		}
	}
	if issues := quality.Issues(q); len(issues) > 0 {
		q.Status = "needs_review"
		q.Warnings = append(q.Warnings, issues...)
	}
	return q, nil
}

func answerChoiceLabels(raw string) []string {
	return choice.Parse(raw)
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
	}
	qs := make([]models.GeneratedQuestion, 0, len(spec.Entries))
	for i, entry := range spec.Entries {
		q, err := g.Generate(ctx, entry)
		if err != nil {
			return nil, fmt.Errorf("generate spec %s: %w", entry.ID, err)
		}
		fmt.Printf("[generate %d/%d] %s %.0f分 题面%d字\n", i+1, len(spec.Entries), entry.Type, entry.Score, len([]rune(q.Stem)))
		qs = append(qs, q)
	}
	return qs, nil
}

// buildSystemPrompt constructs the system prompt for question generation.
func (g *Generator) buildSystemPrompt(spec models.SpecEntry) string {
	typeDesc := typeDescription(spec.Type)
	cogDesc := cognitiveDescription(spec.Cognitive)
	diffDesc := difficultyDescription(spec.DiffBand)

	answerRule := "单选题只能有一个正确选项"
	if spec.Type == models.TypeMultiChoice {
		answerRule = "多选题必须有至少两个正确选项，答案可写成 A,C 或 AC"
	}
	typeRule := questionFormatRule(spec.Type)
	return fmt.Sprintf(`你是一个考研命题专家。你需要根据以下规格生成一道完整、可独立作答且经过验算的考研模拟题:

题型: %s
知识点: %s
认知层次(Bloom): %s
难度目标: %s (band %d，数值必须落在该区间)
分值: %.0f

要求:
1. 题面必须用 LaTeX 格式输出, 不含 \documentclass 或 \begin{document}
2. 题面内容必须严格围绕给定知识点, 不得偏题
3. 难度必须匹配目标难度band (%s)
4. 认知层次必须匹配 (%s) — 不要用记忆题代替应用题
5. 选择题必须提供4个互不重复且非空的选项 (A/B/C/D); %s
6. 必须严格使用题型对应格式：%s
7. 参考答案必须完整但简洁，给出必要步骤；返回前必须独立复核题面条件、计算、选项和最终结论，确保答案与推导及选项完全一致；选择题答案首句必须明确写“答案：A/B/C/D”；不要重复题面，不要输出无关说明
8. 题目必须自洽且信息足以确定答案；禁止在答案中写“题目有误”“假设题意”“修改题面”“重新审视”“如果题目要求”等自我纠错或不确定性说明。若草稿发现条件矛盾，直接重新设计一道自洽的新题后再输出
9. 难度数值必须服从区间：易为 [0.00,0.34]，中为 [0.35,0.64]，难为 [0.65,1.00]；不要把所有题都填写为 0.5
10. 以JSON格式返回且只返回这4个字段: {"stem": "LaTeX题面", "options": ["选项A", "选项B", "选项C", "选项D"], "answer": "答案与解析", "difficulty": 0.75}`,
		typeDesc,
		strings.Join(spec.Points, ", "),
		cogDesc,
		diffDesc,
		spec.DiffBand,
		spec.Score,
		diffDesc,
		cogDesc,
		answerRule,
		typeRule,
	)
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
