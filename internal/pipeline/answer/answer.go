// Package answer implements Stage [5]: 标准答案层.
//
// Architecture: dual scoring Agents + single review Agent, with:
//   - shared rubric (横跨标准答案与批改两阶段的一等数据)
//   - per-checkpoint verdict (not aggregate score)
//   - disagreement threshold + escalation protocol
//   - SymPy independent verification (third check, catches same-family blind spots)
//   - cross-family judge models (generator vs verifier use different model families)
package answer

import (
	"context"
	"fmt"
	"math"
	"strings"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/quality"
)

// DisagreementThreshold: if agents disagree on a checkpoint, escalate to reviewer.
const DisagreementThreshold = 0.3

// ScoringAgent evaluates a question's answer against a rubric, per-checkpoint.
type ScoringAgent struct {
	LLM   *llm.Client
	Model string
}

// CheckpointVerdict is one rubric item's pass/fail + confidence from a scoring agent.
type CheckpointVerdict struct {
	RubricItemID string  `json:"rubricItemId"`
	Passed       bool    `json:"passed"`
	Confidence   float64 `json:"confidence"`
	Note         string  `json:"note"`
}

// ReviewResult is the finalised standard answer + rubric package.
type ReviewResult struct {
	Question       models.GeneratedQuestion `json:"question"`
	Rubric         models.Rubric            `json:"rubric"`
	RubricVerified bool                     `json:"rubricVerified"`
	Verdicts       []CheckpointVerdict      `json:"verdicts"`
	SympyVerified  bool                     `json:"sympyVerified"`
	Finalised      bool                     `json:"finalised"`
}

// AnswerService orchestrates the dual-agent + review + SymPy pipeline.
type AnswerService struct {
	Agent1   *ScoringAgent // cross-family model A (e.g. GLM)
	Agent2   *ScoringAgent // cross-family model B (e.g. Qwen)
	Reviewer *ScoringAgent // review model (can be same as one of above)
}

// New creates an AnswerService with cross-family scoring agents.
func New(llmClient *llm.Client, model1, model2, reviewModel string) *AnswerService {
	return &AnswerService{
		Agent1:   &ScoringAgent{LLM: llmClient, Model: model1},
		Agent2:   &ScoringAgent{LLM: llmClient, Model: model2},
		Reviewer: &ScoringAgent{LLM: llmClient, Model: reviewModel},
	}
}

// rubricResponse is the LLM JSON output for rubric generation.
type rubricResponse struct {
	Items             []rubricItemJSON `json:"items"`
	AcceptableMethods []string         `json:"acceptableMethods"`
}

type rubricItemJSON struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	MaxScore    float64 `json:"maxScore"`
}

// scoringResponse is the LLM JSON output for per-checkpoint scoring.
type scoringResponse struct {
	Verdicts []verdictJSON `json:"verdicts"`
}

type verdictJSON struct {
	RubricItemID string  `json:"rubricItemId"`
	Passed       bool    `json:"passed"`
	Confidence   float64 `json:"confidence"`
	Note         string  `json:"note"`
}

// ProduceAnswer runs the full answer-production pipeline for one question.
//
// Steps:
//  1. Generate rubric checklist from question stem + spec.
//  2. Agent1 + Agent2 independently score per-checkpoint (with confidence).
//  3. Compare verdicts; disagreements → review Agent 裁决.
//  4. If review confidence low → escalation (flag for human review).
//  5. SymPy independent verification (计算题) / sandbox (算法题) — stubbed in MVP.
//  6. Finalise: store answer + rubric + acceptableMethods + partial分细则.
func (s *AnswerService) ProduceAnswer(ctx context.Context, q models.GeneratedQuestion) (*ReviewResult, error) {
	result := &ReviewResult{
		Question: q,
	}
	if strings.TrimSpace(q.ID) == "" || !finitePositive(q.Score) || blankOrPlaceholder(q.Stem) || blankOrPlaceholder(q.Answer) {
		result.Question.Status = "needs_review"
		result.Rubric = models.Rubric{
			ID:         fmt.Sprintf("rubric_%s", q.ID),
			QuestionID: q.ID,
			Items:      fallbackRubricItems(q.Score),
			Version:    1,
		}
		result.Verdicts = unresolvedVerdicts(result.Rubric, "题面、答案或题目分值无效")
		return result, nil
	}

	// Step 1: Generate rubric.
	rubric, rubricVerified, err := s.generateRubric(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("generate rubric for %s: %w", q.ID, err)
	}
	result.Rubric = rubric
	result.RubricVerified = rubricVerified

	// Step 2: Dual-agent scoring.
	verdicts1, err := s.scoreCheckpoints(ctx, s.Agent1, q, rubric)
	if err != nil {
		return nil, fmt.Errorf("agent1 scoring: %w", err)
	}
	verdicts2, err := s.scoreCheckpoints(ctx, s.Agent2, q, rubric)
	if err != nil {
		return nil, fmt.Errorf("agent2 scoring: %w", err)
	}

	// Step 3: Compare and resolve disagreements.
	finalVerdicts := s.resolveDisagreements(ctx, verdicts1, verdicts2, rubric)
	result.Verdicts = finalVerdicts

	// Step 4: Check for low-confidence items needing escalation.
	for i := range finalVerdicts {
		if finalVerdicts[i].Confidence < DisagreementThreshold {
			finalVerdicts[i].Note = "LOW_CONFIDENCE: " + finalVerdicts[i].Note + " (需人工审核)"
		}
	}

	// Step 5: SymPy/sandbox verification is optional and not implemented in
	// this binary yet. Never claim independent verification merely because the
	// dual-agent path completed; callers can see the unverified state and keep
	// the item in review.
	result.SympyVerified = false

	// Step 6: Finalise.
	complete := len(finalVerdicts) == len(rubric.Items) && len(finalVerdicts) > 0
	for _, verdict := range finalVerdicts {
		if !validConfidence(verdict.Confidence) || verdict.Confidence < DisagreementThreshold {
			complete = false
			break
		}
	}
	// SymPy is an optional enhancement in the current binary. Requiring its
	// hard-coded false stub made every calculation question permanently
	// unreviewable. The mandatory gate is now deterministic question quality,
	// a complete rubric, and two-agent agreement; SymPy can still upgrade the
	// evidence when an implementation is available.
	result.Finalised = q.Status != "needs_review" && result.RubricVerified && complete && answerEvidenceReady(q, result)
	return result, nil
}

func answerEvidenceReady(q models.GeneratedQuestion, result *ReviewResult) bool {
	if len(quality.Issues(q)) > 0 || len(result.Verdicts) == 0 {
		return false
	}
	for _, verdict := range result.Verdicts {
		if !verdict.Passed || !validConfidence(verdict.Confidence) || verdict.Confidence < DisagreementThreshold {
			return false
		}
	}
	return true
}

// generateRubric produces a structured rubric from the question stem + spec.
func (s *AnswerService) generateRubric(ctx context.Context, q models.GeneratedQuestion) (models.Rubric, bool, error) {
	rubric := models.Rubric{
		ID:         fmt.Sprintf("rubric_%s", q.ID),
		QuestionID: q.ID,
		Version:    1,
	}

	if s.Agent1 == nil || s.Agent1.LLM == nil {
		// Fallback rubric still has to match the actual question score. A
		// fixed 10-point rubric corrupts choice-question grading and totals.
		rubric.Items = fallbackRubricItems(q.Score)
		rubric.AcceptableMethods = []string{"标准解法"}
		return rubric, false, nil
	}

	system := `你是一个考研评分标准制定专家。根据题目, 生成结构化的评分标准(rubric)。
评分标准应包含可独立评分的检查点, 每个检查点有:
- id: 检查点ID (如 chk_1, chk_2, ...)
- description: 检查点描述 (如 "正确设变量", "应用公式", "化简结果")
- maxScore: 该检查点满分

同时列出可接受的不同解法族 (acceptableMethods)。

以JSON格式返回: {"items": [{"id":"chk_1","description":"...","maxScore":3}], "acceptableMethods": ["方法1", "方法2"]}`

	user := fmt.Sprintf("题目ID: %s\n题型: %s\n知识点: %s\n题面:\n%s\n参考答案:\n%s",
		q.ID, q.Type, strings.Join(q.Points, ", "), q.Stem, q.Answer)

	var resp rubricResponse
	if err := s.Agent1.LLM.ChatJSON(ctx, s.Agent1.Model, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, &resp); err != nil {
		// Fallback rubric.
		rubric.Items = fallbackRubricItems(q.Score)
		rubric.AcceptableMethods = []string{"标准解法"}
		return rubric, false, nil
	}
	if issue := rubricResponseIssue(resp); issue != "" {
		rubric.Items = fallbackRubricItems(q.Score)
		rubric.AcceptableMethods = []string{"标准解法"}
		return rubric, false, nil
	}

	for _, item := range resp.Items {
		rubric.Items = append(rubric.Items, models.RubricItem{
			ID:          item.ID,
			Description: item.Description,
			MaxScore:    item.MaxScore,
		})
	}
	rubric.Items = normalizeRubricItems(rubric.Items, q.Score)
	rubric.AcceptableMethods = resp.AcceptableMethods
	if len(rubric.AcceptableMethods) == 0 {
		rubric.AcceptableMethods = []string{"标准解法"}
	}

	return rubric, true, nil
}

func fallbackRubricItems(score float64) []models.RubricItem {
	if !finitePositive(score) {
		score = 10
	}
	return []models.RubricItem{{ID: "chk_1", Description: "答案正确且完整", MaxScore: score}}
}

// normalizeRubricItems makes the rubric sum exactly to the question score.
// LLMs frequently return rounded checkpoint values or omit IDs; accepting
// those values unchanged makes the generated paper impossible to reconcile
// with its advertised total.
func normalizeRubricItems(items []models.RubricItem, score float64) []models.RubricItem {
	if !finitePositive(score) {
		score = 10
	}
	clean := make([]models.RubricItem, 0, len(items))
	seen := make(map[string]bool)
	for i, item := range items {
		if !finitePositive(item.MaxScore) {
			continue
		}
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" || seen[item.ID] {
			n := i + 1
			for seen[fmt.Sprintf("chk_%d", n)] {
				n++
			}
			item.ID = fmt.Sprintf("chk_%d", n)
		}
		seen[item.ID] = true
		clean = append(clean, item)
	}
	if len(clean) == 0 {
		return fallbackRubricItems(score)
	}
	total := 0.0
	for _, item := range clean {
		total += item.MaxScore
	}
	if !finitePositive(total) {
		return fallbackRubricItems(score)
	}
	for i := range clean {
		clean[i].MaxScore = score * clean[i].MaxScore / total
	}
	// Remove floating-point residue from the final checkpoint.
	adjusted := 0.0
	for i := 0; i < len(clean)-1; i++ {
		adjusted += clean[i].MaxScore
	}
	clean[len(clean)-1].MaxScore = score - adjusted
	return clean
}

func rubricResponseIssue(resp rubricResponse) string {
	if len(resp.Items) == 0 {
		return "评分标准为空"
	}
	seen := make(map[string]struct{}, len(resp.Items))
	total := 0.0
	for _, item := range resp.Items {
		id := strings.TrimSpace(item.ID)
		if id == "" || strings.TrimSpace(item.Description) == "" {
			return "评分标准检查点缺少 ID 或描述"
		}
		if _, exists := seen[id]; exists {
			return "评分标准包含重复检查点"
		}
		seen[id] = struct{}{}
		if !finitePositive(item.MaxScore) {
			return "评分标准包含无效分值"
		}
		total += item.MaxScore
	}
	if !finitePositive(total) {
		return "评分标准总分无效"
	}
	return ""
}

func blankOrPlaceholder(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || strings.HasPrefix(trimmed, "%") || strings.Contains(strings.ToUpper(trimmed), "TODO")
}

// scoreCheckpoints runs a single scoring agent on all rubric checkpoints.
func (s *AnswerService) scoreCheckpoints(ctx context.Context, agent *ScoringAgent, q models.GeneratedQuestion, rubric models.Rubric) ([]CheckpointVerdict, error) {
	if agent == nil || agent.LLM == nil {
		// No LLM means no evidence of correctness. Keep every checkpoint
		// unresolved so a generated answer cannot be silently finalised.
		verdicts := make([]CheckpointVerdict, 0, len(rubric.Items))
		for _, item := range rubric.Items {
			verdicts = append(verdicts, CheckpointVerdict{
				RubricItemID: item.ID,
				Passed:       false,
				Confidence:   0,
				Note:         "未连接评分模型，需人工审核",
			})
		}
		return verdicts, nil
	}

	// Build checkpoint list for the prompt.
	checkpoints := make([]string, 0, len(rubric.Items))
	for _, item := range rubric.Items {
		checkpoints = append(checkpoints, fmt.Sprintf("- %s (%.0f分): %s", item.ID, item.MaxScore, item.Description))
	}

	system := `你是一个考研阅卷专家。请对参考答案按评分标准逐检查点评分。
对每个检查点, 判断参考答案是否满足该检查点的要求:
- passed: true=满足, false=不满足
- confidence: 0~1, 评分置信度
- note: 评分说明

以JSON格式返回: {"verdicts": [{"rubricItemId":"chk_1","passed":true,"confidence":0.9,"note":"步骤完整"}]}`

	user := fmt.Sprintf("题目:\n%s\n\n参考答案:\n%s\n\n评分标准检查点:\n%s",
		q.Stem, q.Answer, strings.Join(checkpoints, "\n"))

	var resp scoringResponse
	if err := agent.LLM.ChatJSON(ctx, agent.Model, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, &resp); err != nil {
		// Fallback: all pass with low confidence.
		verdicts := make([]CheckpointVerdict, 0, len(rubric.Items))
		for _, item := range rubric.Items {
			verdicts = append(verdicts, CheckpointVerdict{
				RubricItemID: item.ID,
				Passed:       false,
				Confidence:   0,
				Note:         "LLM评分失败，需人工审核",
			})
		}
		return verdicts, nil
	}

	if issue := scoringResponseIssue(resp, rubric); issue != "" {
		return unresolvedVerdicts(rubric, issue), nil
	}

	// Ensure we have a verdict for every rubric item.
	verdictMap := make(map[string]verdictJSON)
	for _, v := range resp.Verdicts {
		verdictMap[v.RubricItemID] = v
	}
	verdicts := make([]CheckpointVerdict, 0, len(rubric.Items))
	for _, item := range rubric.Items {
		if v, ok := verdictMap[item.ID]; ok {
			verdicts = append(verdicts, CheckpointVerdict{
				RubricItemID: v.RubricItemID,
				Passed:       v.Passed,
				Confidence:   v.Confidence,
				Note:         v.Note,
			})
		} else {
			verdicts = append(verdicts, CheckpointVerdict{
				RubricItemID: item.ID,
				Passed:       false,
				Confidence:   0,
				Note:         "模型未返回此检查点，需人工审核",
			})
		}
	}

	return verdicts, nil
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validConfidence(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func scoringResponseIssue(resp scoringResponse, rubric models.Rubric) string {
	known := make(map[string]struct{}, len(rubric.Items))
	for _, item := range rubric.Items {
		known[item.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(resp.Verdicts))
	for _, verdict := range resp.Verdicts {
		if _, ok := known[verdict.RubricItemID]; !ok {
			return "评分模型返回未知检查点"
		}
		if _, ok := seen[verdict.RubricItemID]; ok {
			return "评分模型返回重复检查点"
		}
		seen[verdict.RubricItemID] = struct{}{}
		if !validConfidence(verdict.Confidence) {
			return "评分模型返回无效置信度"
		}
	}
	if len(seen) != len(known) {
		return "评分模型未覆盖全部检查点"
	}
	return ""
}

func unresolvedVerdicts(rubric models.Rubric, reason string) []CheckpointVerdict {
	verdicts := make([]CheckpointVerdict, 0, len(rubric.Items))
	for _, item := range rubric.Items {
		verdicts = append(verdicts, CheckpointVerdict{
			RubricItemID: item.ID,
			Passed:       false,
			Confidence:   0,
			Note:         reason + "，需人工审核",
		})
	}
	return verdicts
}

// resolveDisagreements compares two agents' verdicts and escalates
// disagreements to the reviewer.
func (s *AnswerService) resolveDisagreements(ctx context.Context, v1, v2 []CheckpointVerdict, rubric models.Rubric) []CheckpointVerdict {
	// Build maps for lookup.
	map1 := make(map[string]CheckpointVerdict, len(v1))
	for _, v := range v1 {
		map1[v.RubricItemID] = v
	}
	map2 := make(map[string]CheckpointVerdict, len(v2))
	for _, v := range v2 {
		map2[v.RubricItemID] = v
	}

	final := make([]CheckpointVerdict, 0, len(rubric.Items))
	for _, item := range rubric.Items {
		a, ok1 := map1[item.ID]
		b, ok2 := map2[item.ID]

		if !ok1 || !ok2 {
			// A single-agent result is not independent confirmation.
			final = append(final, CheckpointVerdict{
				RubricItemID: item.ID,
				Passed:       false,
				Confidence:   0,
				Note:         "评分代理未完整返回此检查点，需人工审核",
			})
			continue
		}

		// Both agents present: check agreement.
		if a.Passed == b.Passed {
			// Agreement is only as strong as the weaker independent judgment.
			chosen := a
			if b.Confidence > a.Confidence {
				chosen = b
			}
			if a.Confidence < b.Confidence {
				chosen.Confidence = a.Confidence
			} else {
				chosen.Confidence = b.Confidence
			}
			chosen.Note = fmt.Sprintf("agreement: %s", chosen.Note)
			final = append(final, chosen)
		} else {
			// Disagreement — escalate to reviewer.
			resolved := s.escalateToReviewer(ctx, item, a, b)
			final = append(final, resolved)
		}
	}

	return final
}

// escalateToReviewer asks the review agent to break a tie between two agents.
func (s *AnswerService) escalateToReviewer(ctx context.Context, item models.RubricItem, a, b CheckpointVerdict) CheckpointVerdict {
	if s.Reviewer == nil || s.Reviewer.LLM == nil {
		return CheckpointVerdict{RubricItemID: item.ID, Note: "评分代理意见分歧且无审查模型，需人工审核"}
	}

	type reviewerResponse struct {
		Passed     bool    `json:"passed"`
		Confidence float64 `json:"confidence"`
		Note       string  `json:"note"`
	}
	var response reviewerResponse
	system := "你是严格的评分复核员。请根据评分检查点和两个独立判断，给出最终判断。只有证据充分时才给高置信度。返回JSON: {\"passed\":true,\"confidence\":0.0,\"note\":\"...\"}"
	user := fmt.Sprintf("检查点(%s, %.2f分): %s\n判断A: passed=%v confidence=%.2f note=%s\n判断B: passed=%v confidence=%.2f note=%s",
		item.ID, item.MaxScore, item.Description, a.Passed, a.Confidence, a.Note, b.Passed, b.Confidence, b.Note)
	if err := s.Reviewer.LLM.ChatJSON(ctx, s.Reviewer.Model, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, &response); err != nil {
		return CheckpointVerdict{RubricItemID: item.ID, Note: "审查模型调用失败，需人工审核"}
	}
	if !validConfidence(response.Confidence) {
		return CheckpointVerdict{RubricItemID: item.ID, Note: "审查模型返回无效置信度，需人工审核"}
	}
	return CheckpointVerdict{RubricItemID: item.ID, Passed: response.Passed, Confidence: response.Confidence, Note: response.Note}
}

// VerifySympy runs SymPy symbolic verification on a calculation question.
// This is the third independent check that catches same-family LLM blind spots.
//
// The independent verifier is not bundled yet. Returning false is deliberate:
// callers must not treat an unimplemented check as evidence of correctness.
func (s *AnswerService) VerifySympy(ctx context.Context, q models.GeneratedQuestion) (bool, error) {
	return false, nil
}

// VerifySandbox runs property-based tests in a Docker sandbox for algorithm questions.
// Reference solution + independently-written solution must agree on random inputs.
//
// The sandbox is not bundled yet. Returning false keeps the verification
// contract honest until a constrained execution backend is installed.
func (s *AnswerService) VerifySandbox(ctx context.Context, q models.GeneratedQuestion) (bool, error) {
	return false, nil
}
