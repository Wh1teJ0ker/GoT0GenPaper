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
	"encoding/json"
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
	q.Stem = cleanGeneratedStem(normalizeGeneratedNewlines(resp.Stem))
	q.Options = normalizeOptions(resp.Options)
	q.SharedMaterial = cleanGeneratedStem(normalizeGeneratedNewlines(resp.SharedMaterial))
	q.SharedOptions = normalizeOptions(resp.SharedOptions)
	q.Answer = normalizeGeneratedNewlines(resp.Answer)
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
			if models.IsEnglish2PartB(q.Points) {
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
	if isEnglish2Subject(spec.Subject) {
		return g.generateEnglish2All(ctx, spec)
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

func validateEnglish2Spec(entries []models.SpecEntry) (map[int]models.SpecEntry, error) {
	if len(entries) != 48 {
		return nil, fmt.Errorf("英语二按 2026 结构必须有 48 个题位，当前 %d", len(entries))
	}
	byPosition := make(map[int]models.SpecEntry, len(entries))
	for _, entry := range entries {
		position, ok := models.EnglishQuestionPosition(entry.Points)
		if !ok || position > 48 {
			return nil, fmt.Errorf("spec %s 缺少有效的 2026 英语二题位", entry.ID)
		}
		if _, exists := byPosition[position]; exists {
			return nil, fmt.Errorf("2026 英语二题位 %d 重复", position)
		}
		wantType, wantScore := models.TypeChoice, 2.0
		switch {
		case position <= 20:
			wantScore = 0.5
			if len(entry.Points) < 2 || entry.Points[1] != "完形填空" {
				return nil, fmt.Errorf("英语二题位 %d 必须属于完形填空", position)
			}
		case position <= 40:
			textNumber := (position-21)/5 + 1
			if len(entry.Points) < 4 || entry.Points[1] != "阅读理解" || entry.Points[2] != "Part A" || entry.Points[3] != fmt.Sprintf("Text %d", textNumber) {
				return nil, fmt.Errorf("英语二题位 %d 必须属于阅读 Part A Text %d", position, textNumber)
			}
		case position <= 45:
			if !models.IsEnglish2PartB(entry.Points) || len(entry.Points) < 4 ||
				!models.IsEnglish2PartBMatchType(entry.Points[3]) {
				return nil, fmt.Errorf("英语二题位 %d 必须属于阅读 Part B", position)
			}
		case position == 46:
			wantType, wantScore = models.TypeMajor, 15
			if len(entry.Points) < 2 || entry.Points[1] != "翻译" {
				return nil, fmt.Errorf("英语二题位 46 必须是 Part C 整段翻译")
			}
		case position == 47:
			wantType, wantScore = models.TypeMajor, 10
			if len(entry.Points) < 3 || entry.Points[1] != "写作" || entry.Points[2] != "应用文" {
				return nil, fmt.Errorf("英语二题位 47 必须是 Part A 应用文")
			}
		case position == 48:
			wantType, wantScore = models.TypeMajor, 15
			if len(entry.Points) < 3 || entry.Points[1] != "写作" || !strings.Contains(entry.Points[2], "图表") {
				return nil, fmt.Errorf("英语二题位 48 必须是 Part B 图表作文")
			}
		}
		if entry.Type != wantType || math.Abs(entry.Score-wantScore) > 0.01 {
			return nil, fmt.Errorf("英语二题位 %d 的题型/分值错误：%s %.2f，要求 %s %.2f", position, entry.Type, entry.Score, wantType, wantScore)
		}
		byPosition[position] = entry
	}
	for position := 1; position <= 48; position++ {
		if _, exists := byPosition[position]; !exists {
			return nil, fmt.Errorf("2026 英语二缺少题位 %d", position)
		}
	}
	return byPosition, nil
}

func english2Range(byPosition map[int]models.SpecEntry, start, end int) []models.SpecEntry {
	entries := make([]models.SpecEntry, 0, end-start+1)
	for position := start; position <= end; position++ {
		entries = append(entries, byPosition[position])
	}
	return entries
}

func (g *Generator) generateEnglish2All(ctx context.Context, spec models.SpecTable) ([]models.GeneratedQuestion, error) {
	if g.LLM == nil {
		return nil, fmt.Errorf("英语二 2026 复合题型需要已初始化的大模型客户端")
	}
	byPosition, err := validateEnglish2Spec(spec.Entries)
	if err != nil {
		return nil, err
	}
	groups := []struct {
		id      string
		entries []models.SpecEntry
	}{
		{id: "english2-cloze", entries: english2Range(byPosition, 1, 20)},
		{id: "english2-reading-1", entries: english2Range(byPosition, 21, 25)},
		{id: "english2-reading-2", entries: english2Range(byPosition, 26, 30)},
		{id: "english2-reading-3", entries: english2Range(byPosition, 31, 35)},
		{id: "english2-reading-4", entries: english2Range(byPosition, 36, 40)},
		{id: "english2-part-b", entries: english2Range(byPosition, 41, 45)},
	}
	generated := make(map[string]models.GeneratedQuestion, len(spec.Entries))
	completed := 0
	for _, group := range groups {
		questions, err := g.generateEnglish2Group(ctx, spec.Subject, group.id, group.entries)
		if err != nil {
			return nil, fmt.Errorf("生成英语二 %s 题组: %w", group.id, err)
		}
		for _, question := range questions {
			generated[question.SpecID] = question
			completed++
			fmt.Printf("[generate %d/48] %s %.1f分 题面%d字\n", completed, question.Type, question.Score, len([]rune(question.Stem)))
		}
	}
	for _, position := range []int{46, 47, 48} {
		entry := byPosition[position]
		question, err := g.GenerateForSubject(ctx, spec.Subject, entry)
		if err != nil {
			return nil, fmt.Errorf("generate spec %s: %w", entry.ID, err)
		}
		generated[question.SpecID] = question
		completed++
		fmt.Printf("[generate %d/48] %s %.0f分 题面%d字\n", completed, question.Type, question.Score, len([]rune(question.Stem)))
	}
	ordered := make([]models.GeneratedQuestion, 0, len(spec.Entries))
	for _, entry := range spec.Entries {
		question, exists := generated[entry.ID]
		if !exists {
			return nil, fmt.Errorf("英语二题位 %s 未生成", entry.ID)
		}
		ordered = append(ordered, question)
	}
	return ordered, nil
}

func (g *Generator) generateEnglish2Group(ctx context.Context, subject, groupID string, entries []models.SpecEntry) ([]models.GeneratedQuestion, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("题组没有题位")
	}
	request := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		position, ok := models.EnglishQuestionPosition(entry.Points)
		if !ok {
			return nil, fmt.Errorf("spec %s 缺少有效英语题位", entry.ID)
		}
		request = append(request, map[string]any{
			"specId": entry.ID, "number": position, "score": entry.Score,
			"difficultyBand": entry.DiffBand, "knowledgePoints": entry.Points,
		})
	}
	encoded, _ := json.Marshal(request)
	groupRules := ""
	switch groupID {
	case "english2-cloze":
		groupRules = "生成一篇完整、原创、连贯且至少 250 个英文词的完形短文。最重要的硬约束：sharedMaterial 正文中必须实际出现恰好 20 个编号空，字面值依次为 [1]、[2]、[3]……[20]，每个编号各出现一次，不得改成下划线、圆括号、尖括号、空格或其他记号，也不得出现 [21] 等其他编号。请在返回 JSON 前逐个搜索并核对 [1] 至 [20]。每个 item stem 必须准确写为对应的 Blank [n]，不得重写文章；每空提供 4 个词/短语选项。解析须说明正确项的上下文、语法或搭配依据，并逐项排除其余 A-D 选项。不得拆成 20 篇短文；sharedMaterial 只放一篇完整文章。"
	case "english2-part-b":
		groupRules = "严格仿照 2026 年英语二 Part B 人物观点匹配题：sharedMaterial 是一篇至少 250 个英文词的完整英文材料，其中必须清晰介绍或引用 5 位不同的具名人物。sharedOptions 提供 7 条互不重复的英文观点陈述，不要在陈述文本前加 A-G 字母（排版会自动加），其中恰有 2 条干扰项。items 对应第 41-45 题，stem 必须依次填写 5 位人物的完整英文姓名，不能写 Paragraph 1-5、题号、泛称或中文标签；每个姓名必须在 sharedMaterial 中原样出现且五题不得重复。每题 options 必须为空，answer 是 A-G 中唯一一个字母，五题答案不得重复。每题用不少于 30 个汉字解释该人物发言或观点与对应陈述的依据。不得改成普通四选一、段落小标题匹配、段落排序或七选五。"
	default:
		groupRules = "生成一篇完整、原创、连贯且至少 250 个英文词的阅读文章，并针对该文生成 5 道相互关联但考点有区分的四选一题。五题必须全部依据同一篇 sharedMaterial 作答，不得为单题另写文章或在题干重复文章。考查细节、推断、主旨、词义/指代、作者态度等不同能力。每题提供 4 个选项；解析须说明正确项依据并逐项排除其余 A-D 选项。"
	}
	groupType := "阅读理解 Part A 共文题组"
	if groupID == "english2-cloze" {
		groupType = "英语知识运用（完形填空）"
	} else if groupID == "english2-part-b" {
		groupType = "阅读理解 Part B 人物观点匹配"
	}
	user := fmt.Sprintf("题组类型：%s\n题组编号：%s\n题位规格（JSON）：%s\n\n%s\n\n只返回 JSON 对象，结构严格为：{\"sharedMaterial\":\"完整共同材料\",\"sharedOptions\":[],\"items\":[{\"specId\":\"题位 ID\",\"stem\":\"该小题题干\",\"options\":[\"选项A\",\"选项B\",\"选项C\",\"选项D\"],\"answer\":\"答案：X。中文解析……\",\"difficulty\":0.55}]}。题位必须逐项对应，不得增加或遗漏。非 Part B 客观题的 answer 必须以“答案：A/B/C/D”开头，并逐项解释四个选项；每一项前必须使用可识别的标签“ A项：”“B项：”“C项：”“D项：”或“Option A:”“Option B:”“Option C:”“Option D:”，不能只写“其余选项”。Part B 时每题 options 必须为空，七条观点陈述只放 sharedOptions。", groupType, groupID, encoded, groupRules)
	system := "你是熟悉 2026 年全国硕士研究生招生考试英语（二）结构的命题专家。严格生成原创试题，不得照抄历年真题或在同组内切换文章。材料、题干、选项和答案必须自洽，答案字母与解析一致。"
	model := g.selectModel(models.TypeChoice)
	messages := []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
	var response english2GroupResponse
	err := g.LLM.ChatJSON(ctx, model, messages, &response)
	if err != nil && g.FallbackModel != "" && g.FallbackModel != model {
		err = g.LLM.ChatJSON(ctx, g.FallbackModel, messages, &response)
	}
	if err != nil {
		return nil, err
	}
	if issue := validateEnglish2GroupResponse(groupID, entries, response); issue != "" {
		lastIssue := issue
		for attempt := 1; attempt <= 2; attempt++ {
			messages[1].Content = user + fmt.Sprintf("\n\n上一响应未通过第 %d 次结构校验：%s。请完整重做，不要修补或省略字段。对于完形题，sharedMaterial 必须逐字包含 [1] [2] [3] …… [20] 这 20 个编号空；如果上一版用了下划线或其他符号，必须全部改成方括号编号。返回前逐个核对每个编号恰好出现一次。", attempt, lastIssue)
			var retry english2GroupResponse
			if retryErr := g.LLM.ChatJSON(ctx, model, messages, &retry); retryErr != nil {
				lastIssue = fmt.Sprintf("结构修复请求失败: %v", retryErr)
				continue
			}
			if retryIssue := validateEnglish2GroupResponse(groupID, entries, retry); retryIssue != "" {
				lastIssue = retryIssue
				continue
			}
			response = retry
			lastIssue = ""
			break
		}
		if lastIssue != "" {
			return nil, fmt.Errorf("题组结构不合格: %s", lastIssue)
		}
	}
	finishGroup := func(groupResponse english2GroupResponse) ([]models.GeneratedQuestion, error) {
		items := make(map[string]english2GroupItem, len(groupResponse.Items))
		for _, item := range groupResponse.Items {
			items[item.SpecID] = item
		}
		questions := make([]models.GeneratedQuestion, 0, len(entries))
		for _, entry := range entries {
			item := items[entry.ID]
			options := item.Options
			if groupID == "english2-part-b" {
				options = groupResponse.SharedOptions
			}
			question, err := g.finishForSubject(subject, entry, generationResponse{
				Stem: item.Stem, Options: options, Answer: item.Answer, Difficulty: item.Difficulty,
				SharedMaterial: groupResponse.SharedMaterial, SharedOptions: groupResponse.SharedOptions,
			})
			if err != nil {
				return nil, err
			}
			question.GroupID = groupID
			if question.Status != "ready" {
				return nil, fmt.Errorf("spec %s 校验失败: %s", entry.ID, strings.Join(question.Warnings, "；"))
			}
			questions = append(questions, question)
		}
		return questions, nil
	}
	questions, finishErr := finishGroup(response)
	if finishErr == nil {
		return questions, nil
	}

	// Structure can be valid while one item's explanation or answer quality is
	// not. Regenerate the whole shared-material group up to two times so a
	// single weak item cannot leak into a composite paper.
	lastErr := finishErr
	for attempt := 1; attempt <= 2; attempt++ {
		messages[1].Content = user + fmt.Sprintf("\n\n题组结构已正确，但第 %d 次单题质量校验失败：%s。请整组重新生成；每道题的答案都必须给出清晰中文解析、关键依据，并按要求逐项排除 A-D 干扰项，不能只写结论。", attempt, lastErr)
		var qualityRetry english2GroupResponse
		if retryErr := g.LLM.ChatJSON(ctx, model, messages, &qualityRetry); retryErr != nil {
			lastErr = fmt.Errorf("题组质量修复请求失败: %w", retryErr)
			continue
		}
		if retryIssue := validateEnglish2GroupResponse(groupID, entries, qualityRetry); retryIssue != "" {
			lastErr = fmt.Errorf("题组质量修复后结构不合格: %s", retryIssue)
			continue
		}
		questions, finishErr = finishGroup(qualityRetry)
		if finishErr == nil {
			return questions, nil
		}
		lastErr = finishErr
	}
	return nil, fmt.Errorf("题组质量修复后仍未通过: %w", lastErr)
}

func validateEnglish2GroupResponse(groupID string, entries []models.SpecEntry, response english2GroupResponse) string {
	if strings.TrimSpace(response.SharedMaterial) == "" {
		return "缺少 sharedMaterial"
	}
	if wordCount := len(englishGroupWordPattern.FindAllString(response.SharedMaterial, -1)); wordCount < 180 {
		return fmt.Sprintf("共用材料仅 %d 个英文词，至少需要 180 词", wordCount)
	}
	if len(response.Items) != len(entries) {
		return fmt.Sprintf("题目数量为 %d，要求 %d", len(response.Items), len(entries))
	}
	if groupID == "english2-cloze" {
		markers := make(map[int]int, 20)
		for _, match := range englishClozeMarkerPattern.FindAllStringSubmatch(response.SharedMaterial, -1) {
			var number int
			if _, err := fmt.Sscanf(match[1], "%d", &number); err != nil || number < 1 || number > 20 {
				return "完形文章只能包含 [1] 至 [20] 编号空"
			}
			markers[number]++
		}
		for number := 1; number <= 20; number++ {
			if markers[number] != 1 {
				return fmt.Sprintf("完形文章中的 [%d] 空位出现 %d 次，要求恰好一次", number, markers[number])
			}
		}
	}
	if groupID == "english2-part-b" && len(response.SharedOptions) != 7 {
		return fmt.Sprintf("Part B 小标题数量为 %d，要求 7", len(response.SharedOptions))
	}
	wanted := make(map[string]bool, len(entries))
	positions := make(map[string]int, len(entries))
	for _, entry := range entries {
		wanted[entry.ID] = true
		if position, ok := models.EnglishQuestionPosition(entry.Points); ok {
			positions[entry.ID] = position
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
		if groupID == "english2-part-b" {
			if len(item.Options) != 0 {
				return "Part B 单题选项必须留空，A-G 观点陈述只能作为共享选项出现一次"
			}
			target := strings.TrimSpace(item.Stem)
			if !partBPersonNamePattern.MatchString(target) {
				return fmt.Sprintf("Part B 题位 %s 必须填写一位完整英文姓名，不能使用 Paragraph、题号或其他题型", item.SpecID)
			}
			if !strings.Contains(strings.ToLower(response.SharedMaterial), strings.ToLower(target)) {
				return fmt.Sprintf("Part B 题位 %s 的人物姓名未出现在 sharedMaterial 中", item.SpecID)
			}
			answer := choice.ParseWithAlphabet(item.Answer, "ABCDEFG")
			if issue := choice.ValidateWithAlphabet(answer, false, "ABCDEFG"); issue != "" {
				return fmt.Sprintf("Part B 题位 %s 答案无效：%s", item.SpecID, issue)
			}
			if partBAnswers[answer[0]] {
				return "Part B 五题答案必须一一匹配，不得重复使用同一标题"
			}
			partBAnswers[answer[0]] = true
		} else if len(item.Options) != 4 {
			return fmt.Sprintf("题位 %s 的四选项数量为 %d", item.SpecID, len(item.Options))
		}
		if groupID == "english2-cloze" && !strings.Contains(item.Stem, fmt.Sprintf("[%d]", positions[item.SpecID])) {
			return fmt.Sprintf("完形题位 %s 的题干必须准确引用对应的编号空", item.SpecID)
		}
	}
	if groupID == "english2-part-b" {
		seenOptions := make(map[string]bool, 7)
		for _, option := range response.SharedOptions {
			value := strings.TrimSpace(option)
			if value == "" || seenOptions[value] || partBStatementPrefixPattern.MatchString(value) {
				return "Part B 观点陈述为空、重复或已带 A-G 标签"
			}
			seenOptions[value] = true
		}
	}
	return ""
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
	return fmt.Sprintf(`你是一个考研命题专家。你需要根据以下规格生成一道完整、可独立作答且经过验算的考研模拟题:

题型: %s
知识点: %s
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
8. 题目必须自洽且信息足以确定答案；禁止在答案中写“题目有误”“假设题意”“修改题面”“重新审视”“如果题目要求”等自我纠错或不确定性说明。若草稿发现条件矛盾，直接重新设计一道自洽的新题后再输出
9. 难度数值必须服从区间：易为 [0.00,0.34]，中为 [0.35,0.64]，难为 [0.65,1.00]；不要把所有题都填写为 0.5
10. 以JSON格式返回且只返回这4个字段: {"stem": "LaTeX题面", "options": ["选项A", "选项B", "选项C", "选项D"], "answer": "答案与解析", "difficulty": 0.75}
11. %s`,
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
		subjectRule,
	)
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
	if models.IsEnglish2PartB(spec.Points) {
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
			return "408 题必须给出足以复核的完整条件；图算法须明确有向/无向、起点、邻接访问次序及复杂度覆盖范围。DFS/BFS 题逐步跟踪访问序列、队列/递归栈及生成树；有向图环检测不能只凭 visited 布尔标志，须区分递归栈/颜色状态与已访问状态。解答题逐问作答并展示关键中间状态和复杂度推导，不得把‘阻止重复访问’误说成‘识别有向环’。"
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
