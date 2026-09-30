// Package grader implements Stage [8]: 批改层.
//
// Uses the SAME rubric as Stage [5] (versioned, shared one-等-data).
// Key differences from answer-stage scoring:
//   - evaluates student work (not LLM-generated reference)
//   - 解法等价判定: student may use different method → check acceptableMethods
//   - 部分分+错误传播: step-level alignment, partial credit before error point
//   - output: per-checkpoint verdict + error-source annotation (not just total score)
package grader

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/choice"
)

// Grader runs student-answer grading with shared rubric.
type Grader struct {
	LLM   *llm.Client
	Model string
}

// New creates a Grader.
func New(llmClient *llm.Client, model string) *Grader {
	return &Grader{LLM: llmClient, Model: model}
}

// GradeRequest is one student's submission for one question.
type GradeRequest struct {
	QuestionID     string   `json:"questionId"`
	StudentAnswer  string   `json:"studentAnswer"`          // raw student work
	QuestionStem   string   `json:"questionStem"`           // 题面(for context)
	MaxScore       float64  `json:"maxScore"`               // 满分(for LLM context)
	AnswerStatus   string   `json:"answerStatus,omitempty"` // recognized / blank / ambiguous / needs_review
	Confidence     float64  `json:"confidence,omitempty"`
	QuestionType   string   `json:"questionType,omitempty"`
	CorrectAnswer  string   `json:"correctAnswer,omitempty"`
	StudentOptions []string `json:"studentOptions,omitempty"`
}

// GradeResult is the full per-checkpoint verdict.
type GradeResult struct {
	QuestionID  string                 `json:"questionId"`
	Verdicts    []models.GradingResult `json:"verdicts"`
	TotalScore  float64                `json:"totalScore"`
	ErrorSource string                 `json:"errorSource"` // 误差来源标注(错在哪步)
	Method      string                 `json:"method"`      // 学生的解法族
	Status      string                 `json:"status"`      // graded / blank / needs_review
	AutoScored  bool                   `json:"autoScored"`
}

// alignmentResponse is the LLM JSON output for step-level alignment.
type alignmentResponse struct {
	Checkpoints []checkpointVerdict `json:"checkpoints"`
	Method      string              `json:"method"`
	ErrorSource string              `json:"errorSource"`
}

// checkpointVerdict is one rubric checkpoint's pass/fail/partial.
type checkpointVerdict struct {
	RubricItemID string  `json:"rubricItemId"`
	Passed       bool    `json:"passed"`
	PartialScore float64 `json:"partialScore"`
	Note         string  `json:"note"`
}

// Grade evaluates a student answer against the shared rubric.
//
// Pipeline:
//  1. Build alignment prompt: rubric checkpoints + student answer + acceptable methods
//  2. LLM maps student work to checkpoints, returns per-checkpoint verdict
//  3. 解法等价判定: if student method ∉ acceptableMethods, flag but don't auto-fail
//  4. Error propagation: if checkpoint[i] fails, downstream checkpoints that depend
//     on it get partial credit based on method validity, not zero
//  5. Total = Σ checkpoint partial scores (NOT LLM-given aggregate)
func (g *Grader) Grade(ctx context.Context, req GradeRequest, rubric models.Rubric) (*GradeResult, error) {
	result := &GradeResult{QuestionID: req.QuestionID, Status: "needs_review"}

	if issue := rubricIssue(rubric); issue != "" {
		result.ErrorSource = issue
		result.Verdicts = reviewVerdicts(rubric, issue)
		return result, nil
	}
	rubricTotal := rubricScore(rubric)
	if math.IsNaN(req.MaxScore) || math.IsInf(req.MaxScore, 0) || (req.MaxScore > 0 && math.Abs(req.MaxScore-rubricTotal) > 0.01) {
		result.ErrorSource = "提交的题目满分与评分标准不一致"
		result.Verdicts = reviewVerdicts(rubric, result.ErrorSource)
		return result, nil
	}
	if strings.EqualFold(req.QuestionType, string(models.TypeChoice)) || strings.EqualFold(req.QuestionType, string(models.TypeMultiChoice)) {
		return gradeObjective(req, rubric), nil
	}

	// Scan callers must explicitly opt into automatic grading. In
	// particular, an ambiguous mark or an image-processing failure must never be sent to
	// the generic text grader and then silently converted to a score.
	answerStatus := strings.ToLower(strings.TrimSpace(req.AnswerStatus))
	switch answerStatus {
	case "recognized":
		if !validRecognitionConfidence(req.Confidence) {
			result.ErrorSource = "识别置信度无效或不足，需人工复核"
			result.Verdicts = reviewVerdicts(rubric, result.ErrorSource)
			return result, nil
		}
		if strings.TrimSpace(req.StudentAnswer) == "" {
			req.AnswerStatus = "blank"
		}
		if req.AnswerStatus == "blank" {
			result.Status = "blank"
			result.AutoScored = true
			result.ErrorSource = "学生未作答"
			for _, item := range rubric.Items {
				result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "空白答案", PartialScore: 0})
			}
			return result, nil
		}
	case "blank":
		result.Status = "blank"
		result.AutoScored = true
		result.ErrorSource = "学生未作答"
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "空白答案", PartialScore: 0})
		}
		return result, nil
	case "ambiguous", "needs_review":
		result.ErrorSource = "答案识别不确定，需人工复核"
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "未自动评分", PartialScore: 0})
		}
		return result, nil
	case "":
	default:
		result.ErrorSource = "答案状态无效，需人工复核"
		result.Verdicts = reviewVerdicts(rubric, result.ErrorSource)
		return result, nil
	}

	if g.LLM == nil {
		return g.fallbackGrade(req, rubric), nil
	}

	// 1. Build and send alignment prompt.
	system := g.buildSystemPrompt(rubric)
	user := g.buildUserPrompt(req, rubric)

	model := g.Model
	if model == "" {
		model = "qwen-plus"
	}

	var resp alignmentResponse
	if err := g.LLM.ChatJSON(ctx, model, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, &resp); err != nil {
		// LLM failure → fallback.
		return g.fallbackGrade(req, rubric), nil
	}

	knownItems := make(map[string]struct{}, len(rubric.Items))
	for _, item := range rubric.Items {
		knownItems[item.ID] = struct{}{}
	}
	malformedResponse := false
	seenResponse := make(map[string]struct{}, len(resp.Checkpoints))
	for _, checkpoint := range resp.Checkpoints {
		if _, known := knownItems[checkpoint.RubricItemID]; !known {
			malformedResponse = true
			continue
		}
		if _, duplicate := seenResponse[checkpoint.RubricItemID]; duplicate {
			malformedResponse = true
		}
		seenResponse[checkpoint.RubricItemID] = struct{}{}
		if math.IsNaN(checkpoint.PartialScore) || math.IsInf(checkpoint.PartialScore, 0) {
			malformedResponse = true
		}
		if partialScoreContradictsVerdict(checkpoint, rubric) {
			malformedResponse = true
		}
	}

	// 2. Map LLM verdicts to GradingResult.
	verdictMap := make(map[string]checkpointVerdict)
	for _, cv := range resp.Checkpoints {
		verdictMap[cv.RubricItemID] = cv
	}

	var totalScore float64
	evaluated := 0
	failedAt := ""
	for _, item := range rubric.Items {
		cv, ok := verdictMap[item.ID]
		if !ok {
			// Checkpoint not evaluated → treat as failed.
			result.Verdicts = append(result.Verdicts, models.GradingResult{
				RubricItemID: item.ID,
				Passed:       false,
				PartialScore: 0,
				Note:         "未评估此检查点",
			})
			if failedAt == "" {
				failedAt = item.ID
			}
			continue
		}

		// Clamp partial score to [0, maxScore].
		partial := cv.PartialScore
		if math.IsNaN(partial) || math.IsInf(partial, 0) {
			partial = 0
			malformedResponse = true
		}
		if partial < 0 || partial > item.MaxScore {
			malformedResponse = true
		}
		if partial < 0 {
			partial = 0
		}
		if partial > item.MaxScore {
			partial = item.MaxScore
		}

		result.Verdicts = append(result.Verdicts, models.GradingResult{
			RubricItemID: item.ID,
			Passed:       cv.Passed,
			PartialScore: partial,
			Note:         cv.Note,
		})
		evaluated++
		totalScore += partial

		if !cv.Passed && failedAt == "" {
			failedAt = item.ID
		}
	}

	// 3. 解法等价判定.
	result.Method = resp.Method
	if resp.Method != "" && len(rubric.AcceptableMethods) > 0 {
		if !methodIsAcceptable(resp.Method, rubric.AcceptableMethods) {
			if result.ErrorSource == "" {
				result.ErrorSource = fmt.Sprintf("解法族不匹配: 学生使用'%s', 可接受: %s",
					resp.Method, strings.Join(rubric.AcceptableMethods, ", "))
			}
		}
	}

	// 4. Error source annotation.
	if failedAt != "" && result.ErrorSource == "" {
		result.ErrorSource = fmt.Sprintf("在检查点 %s 处出错", failedAt)
	} else if resp.ErrorSource != "" && result.ErrorSource == "" {
		result.ErrorSource = resp.ErrorSource
	}

	// 5. Total = sum of checkpoint partial scores.
	result.TotalScore = totalScore
	if malformedResponse {
		// A malformed model response may contain a plausible partial sum, but it
		// is not an auditable score. Keep checkpoint evidence for the reviewer
		// and expose zero as the automatic total.
		result.TotalScore = 0
		result.Status = "needs_review"
		result.AutoScored = false
		result.ErrorSource = "评分模型返回未知、重复或无效检查点"
	} else if evaluated == len(rubric.Items) {
		result.Status = "graded"
		result.AutoScored = true
	} else {
		result.TotalScore = 0
		result.Status = "needs_review"
		result.ErrorSource = "评分模型未覆盖全部检查点"
	}

	return result, nil
}

// gradeObjective is deterministic. Objective answers do not need an LLM, and
// routing them through a language model makes a clearly wrong mark dependent
// on prose interpretation. Ambiguous scan states remain pending review.
func gradeObjective(req GradeRequest, rubric models.Rubric) *GradeResult {
	result := &GradeResult{QuestionID: req.QuestionID, Status: "needs_review", Method: "deterministic-omr"}
	status := strings.ToLower(strings.TrimSpace(req.AnswerStatus))
	if status != "" && status != "ambiguous" && status != "needs_review" && status != "recognized" && status != "blank" {
		result.ErrorSource = "客观题答案状态无效，需人工复核"
		result.Verdicts = reviewVerdicts(rubric, result.ErrorSource)
		return result
	}
	if status == "ambiguous" || status == "needs_review" {
		result.ErrorSource = "客观题识别不确定，需人工复核"
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "未自动评分"})
		}
		return result
	}
	if strings.EqualFold(strings.TrimSpace(req.AnswerStatus), "recognized") && !validRecognitionConfidence(req.Confidence) {
		result.ErrorSource = "客观题识别置信度无效或不足，需人工复核"
		result.Verdicts = reviewVerdicts(rubric, result.ErrorSource)
		return result
	}
	if strings.EqualFold(strings.TrimSpace(req.AnswerStatus), "blank") || (strings.TrimSpace(req.StudentAnswer) == "" && len(req.StudentOptions) == 0) {
		result.Status = "blank"
		result.ErrorSource = "学生未作答"
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "空白答案"})
		}
		return result
	}
	student := append([]string(nil), req.StudentOptions...)
	if len(student) == 0 {
		student = parseChoiceLabels(req.StudentAnswer)
	}
	student = normalizeChoiceLabels(student)
	if issue := choiceLabelsIssue(student, strings.EqualFold(strings.TrimSpace(req.QuestionType), string(models.TypeMultiChoice))); issue != "" {
		result.ErrorSource = issue
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "选项格式无效，未自动评分"})
		}
		return result
	}
	if strings.EqualFold(strings.TrimSpace(req.QuestionType), string(models.TypeChoice)) && len(student) > 1 {
		result.ErrorSource = "单选题检测到多个答案，需人工复核"
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "检测到多个选项，未自动评分"})
		}
		return result
	}
	correct := parseChoiceLabels(req.CorrectAnswer)
	correct = normalizeChoiceLabels(correct)
	if len(correct) == 0 {
		result.ErrorSource = "试卷缺少可解析的标准答案"
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "标准答案需人工确认"})
		}
		return result
	}
	if issue := choiceLabelsIssue(correct, strings.EqualFold(strings.TrimSpace(req.QuestionType), string(models.TypeMultiChoice))); issue != "" {
		result.ErrorSource = "标准答案选项格式无效，需人工确认"
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Note: "标准答案格式无效"})
		}
		return result
	}
	matched := sameChoiceLabels(student, correct)
	result.Status = "graded"
	result.AutoScored = true
	if matched {
		result.TotalScore = rubricScore(rubric)
	}
	for _, item := range rubric.Items {
		partial := 0.0
		if matched {
			partial = item.MaxScore
		}
		note := "客观题答案不匹配"
		if matched {
			note = "客观题答案完全匹配"
		}
		result.Verdicts = append(result.Verdicts, models.GradingResult{RubricItemID: item.ID, Passed: matched, PartialScore: partial, Note: note})
	}
	if !matched {
		result.ErrorSource = "客观题答案不匹配"
	}
	return result
}

func validRecognitionConfidence(confidence float64) bool {
	return !math.IsNaN(confidence) && !math.IsInf(confidence, 0) && confidence >= 0.5 && confidence <= 1
}

func partialScoreContradictsVerdict(verdict checkpointVerdict, rubric models.Rubric) bool {
	for _, item := range rubric.Items {
		if item.ID != verdict.RubricItemID {
			continue
		}
		fullyScored := math.Abs(verdict.PartialScore-item.MaxScore) <= 0.01
		if verdict.Passed != fullyScored {
			return true
		}
		return false
	}
	return true
}

func rubricScore(rubric models.Rubric) float64 {
	total := 0.0
	for _, item := range rubric.Items {
		total += item.MaxScore
	}
	return total
}

func parseChoiceLabels(raw string) []string {
	return choice.Parse(raw)
}

func sameChoiceLabels(a, b []string) bool {
	a = normalizeChoiceLabels(a)
	b = normalizeChoiceLabels(b)
	return choice.Same(a, b)
}

func normalizeChoiceLabels(labels []string) []string {
	result := make([]string, 0, len(labels))
	for _, raw := range labels {
		result = append(result, strings.ToUpper(strings.TrimSpace(raw)))
	}
	return result
}

func choiceLabelsIssue(labels []string, multi bool) string {
	return choice.Validate(labels, multi)
}

func rubricIssue(rubric models.Rubric) string {
	if len(rubric.Items) == 0 {
		return "rubric为空,无法评分"
	}
	seen := make(map[string]struct{}, len(rubric.Items))
	total := 0.0
	for _, item := range rubric.Items {
		if strings.TrimSpace(item.ID) == "" {
			return "rubric包含空检查点ID"
		}
		if _, exists := seen[item.ID]; exists {
			return "rubric包含重复检查点: " + item.ID
		}
		seen[item.ID] = struct{}{}
		if item.MaxScore <= 0 || math.IsNaN(item.MaxScore) || math.IsInf(item.MaxScore, 0) {
			return "rubric包含无效检查点分值: " + item.ID
		}
		total += item.MaxScore
	}
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return "rubric总分无效"
	}
	return ""
}

func reviewVerdicts(rubric models.Rubric, reason string) []models.GradingResult {
	verdicts := make([]models.GradingResult, 0, len(rubric.Items))
	for _, item := range rubric.Items {
		verdicts = append(verdicts, models.GradingResult{RubricItemID: item.ID, Note: "未自动评分: " + reason})
	}
	return verdicts
}

// GradeAll grades a full student submission against the paper's rubrics.
func (g *Grader) GradeAll(ctx context.Context, reqs []GradeRequest, rubrics []models.Rubric) ([]GradeResult, error) {
	rubricMap := make(map[string]models.Rubric)
	duplicateRubrics := make(map[string]bool)
	orderedIDs := make([]string, 0, len(rubrics))
	for _, r := range rubrics {
		if r.QuestionID == "" {
			continue
		}
		if _, exists := rubricMap[r.QuestionID]; !exists {
			orderedIDs = append(orderedIDs, r.QuestionID)
		} else {
			duplicateRubrics[r.QuestionID] = true
			continue
		}
		rubricMap[r.QuestionID] = r
	}
	requests := make(map[string]GradeRequest, len(reqs))
	duplicates := make(map[string]bool)
	var unknown []GradeRequest
	for _, req := range reqs {
		if _, known := rubricMap[req.QuestionID]; !known {
			unknown = append(unknown, req)
			continue
		}
		if _, exists := requests[req.QuestionID]; exists {
			duplicates[req.QuestionID] = true
			continue
		}
		requests[req.QuestionID] = req
	}
	results := make([]GradeResult, 0, len(orderedIDs)+len(unknown))
	for _, id := range orderedIDs {
		rubric := rubricMap[id]
		if duplicateRubrics[id] {
			results = append(results, needsReviewResult(id, rubric, "评分标准重复定义"))
			continue
		}
		if duplicates[id] {
			results = append(results, needsReviewResult(id, rubric, "同一道题存在多个提交结果"))
			continue
		}
		req, exists := requests[id]
		if !exists {
			results = append(results, needsReviewResult(id, rubric, "未提供该题的答题结果"))
			continue
		}
		res, err := g.Grade(ctx, req, rubric)
		if err != nil {
			return nil, err
		}
		results = append(results, *res)
	}
	for _, req := range unknown {
		results = append(results, GradeResult{
			QuestionID:  req.QuestionID,
			Status:      "needs_review",
			ErrorSource: "试卷中没有对应评分标准",
			Verdicts:    []models.GradingResult{},
		})
	}
	return results, nil
}

func needsReviewResult(questionID string, rubric models.Rubric, reason string) GradeResult {
	return GradeResult{
		QuestionID:  questionID,
		Status:      "needs_review",
		ErrorSource: reason,
		Verdicts:    reviewVerdicts(rubric, reason),
	}
}

// --- Prompt builders ---

func (g *Grader) buildSystemPrompt(rubric models.Rubric) string {
	var b strings.Builder
	b.WriteString("你是一个考研阅卷专家。你的任务是将学生解答对齐到评分检查点(rubric), 并给出每步的得分。\n\n")
	b.WriteString("评分规则:\n")
	b.WriteString("1. 将学生解答逐步对齐到每个检查点\n")
	b.WriteString("2. 每个检查点判定: passed(完全正确)/partial(部分正确)/failed(错误)\n")
	b.WriteString("3. partialScore = 该检查点获得的分数(0到maxScore之间)\n")
	b.WriteString("4. 错误传播: 如果某步出错, 后续依赖此步的检查点可得部分分(方法正确但数值错误)\n")
	b.WriteString("5. 如果学生使用的解法与参考解法不同, 在method字段标注学生解法名\n")

	if len(rubric.AcceptableMethods) > 0 {
		b.WriteString(fmt.Sprintf("可接受解法族: %s\n", strings.Join(rubric.AcceptableMethods, ", ")))
	}

	b.WriteString("\n请以JSON格式返回:\n")
	b.WriteString(`{"checkpoints": [{"rubricItemId": "cp1", "passed": true, "partialScore": 3, "note": "步骤正确"}], "method": "换元法", "errorSource": "在cp2处计算错误"}`)

	return b.String()
}

func (g *Grader) buildUserPrompt(req GradeRequest, rubric models.Rubric) string {
	var b strings.Builder
	if req.QuestionStem != "" {
		b.WriteString(fmt.Sprintf("题目:\n%s\n\n", req.QuestionStem))
	}
	b.WriteString(fmt.Sprintf("满分: %.0f\n\n", req.MaxScore))

	b.WriteString("评分检查点:\n")
	for _, item := range rubric.Items {
		b.WriteString(fmt.Sprintf("- %s (%.0f分): %s\n", item.ID, item.MaxScore, item.Description))
	}

	b.WriteString(fmt.Sprintf("\n学生解答:\n%s\n", req.StudentAnswer))

	return b.String()
}

// --- Helpers ---

// fallbackGrade is deliberately conservative: an unavailable LLM cannot
// establish correctness. Non-empty answers therefore remain pending manual
// review instead of receiving the historically unsafe default full score.
func (g *Grader) fallbackGrade(req GradeRequest, rubric models.Rubric) *GradeResult {
	result := &GradeResult{QuestionID: req.QuestionID, Method: "fallback", Status: "needs_review"}
	if strings.TrimSpace(req.StudentAnswer) == "" {
		result.Status = "blank"
		result.AutoScored = true
		for _, item := range rubric.Items {
			result.Verdicts = append(result.Verdicts, models.GradingResult{
				RubricItemID: item.ID,
				Passed:       false,
				PartialScore: 0,
				Note:         "未作答",
			})
		}
		result.ErrorSource = "学生未作答"
		return result
	}
	for _, item := range rubric.Items {
		result.Verdicts = append(result.Verdicts, models.GradingResult{
			RubricItemID: item.ID,
			Passed:       false,
			PartialScore: 0,
			Note:         "评分服务不可用，需人工复核",
		})
	}
	result.ErrorSource = "LLM评分服务不可用，无法确认非空答案正确性"
	return result
}

// methodIsAcceptable checks if the student's method matches any acceptable method.
func methodIsAcceptable(studentMethod string, acceptable []string) bool {
	lower := strings.ToLower(strings.TrimSpace(studentMethod))
	if lower == "" {
		return true
	}
	for _, a := range acceptable {
		candidate := strings.ToLower(strings.TrimSpace(a))
		if candidate != "" && strings.Contains(lower, candidate) {
			return true
		}
	}
	return false
}

// MarshalJSON ensures Verdicts is non-nil for JSON output.
func (r *GradeResult) MarshalJSON() ([]byte, error) {
	type alias GradeResult
	tmp := (*alias)(r)
	if tmp.Verdicts == nil {
		tmp.Verdicts = []models.GradingResult{}
	}
	return json.Marshal(tmp)
}
