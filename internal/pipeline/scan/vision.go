package scan

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

type visionGradeResponse struct {
	QuestionID string   `json:"questionId"`
	Status     string   `json:"status"`
	Selected   []string `json:"selected"`
	Score      float64  `json:"score"`
	Confidence float64  `json:"confidence"`
	Note       string   `json:"note"`
}

// GradeScanWithVision grades the answer-sheet segments with a multimodal LLM.
// The local scanner is responsible for measuring and recording the segment;
// it never substitutes fixed coordinates for visual judgement. A model result
// is accepted only after strict local validation, otherwise the question stays
// needs_review with zero automatic score.
func GradeScanWithVision(ctx context.Context, paper models.ExamPaper, scanResult ScanResult, client *llm.Client, model string) ScanGradeReport {
	report := ScanGradeReport{Scan: scanResult, Results: make([]ScanGradeResult, 0, len(paper.Questions)), NeedsReview: make([]string, 0), Warnings: make([]string, 0), Mode: "segmented-vision"}
	for _, question := range paper.Questions {
		if validQuestionScore(question.Score) {
			report.MaxScore += question.Score
		}
	}
	if client == nil || strings.TrimSpace(model) == "" {
		report.Warnings = append(report.Warnings, "未配置视觉 LLM，分割区域未自动评分")
		for _, question := range paper.Questions {
			appendVisionReview(&report, question, "视觉 LLM 未初始化")
		}
		return report
	}
	if expected := paperFingerprint(paper); expected != "" && scanResult.PaperFingerprint != expected {
		report.Warnings = append(report.Warnings, "扫描结果所属试卷版面指纹不匹配，视觉评分停止")
		for _, question := range paper.Questions {
			appendVisionReview(&report, question, "扫描结果与当前试卷不匹配")
		}
		return report
	}
	for _, page := range scanResult.Pages {
		if page.Status == StatusNeedsReview || !page.Calibrated {
			report.Warnings = append(report.Warnings, "存在未完成校准的扫描页，相关题目不会自动评分")
		}
	}
	segments := make(map[string]AnswerSegment, len(scanResult.Segments))
	for _, segment := range scanResult.Segments {
		if _, exists := segments[segment.QuestionID]; exists {
			report.Warnings = append(report.Warnings, fmt.Sprintf("题目 %s 存在重复答题区域分割", segment.QuestionID))
			continue
		}
		segments[segment.QuestionID] = segment
	}
	for _, question := range paper.Questions {
		segment, exists := segments[question.ID]
		if !exists || segment.Status != StatusRecognized || segment.ImagePath == "" {
			appendVisionReview(&report, question, "缺少可用的当前扫描图分割区域")
			continue
		}
		result, err := gradeSegmentWithVision(ctx, client, model, paper, question, segment)
		if err != nil {
			appendVisionReview(&report, question, err.Error())
			continue
		}
		if result.QuestionID != question.ID {
			appendVisionReview(&report, question, "视觉模型返回了错误题号")
			continue
		}
		if result.Status != "graded" && result.Status != "blank" {
			appendVisionReview(&report, question, firstNonEmpty(result.Note, "视觉模型未确认评分"))
			continue
		}
		if !validVisionScore(result.Score, question.Score) || !validVisionConfidence(result.Confidence) {
			appendVisionReview(&report, question, "视觉模型分数或置信度无效")
			continue
		}
		if (question.Type == models.TypeChoice || question.Type == models.TypeMultiChoice) && result.Status != StatusBlank {
			if issue := validateVisionOptions(result.Selected, question.Type == models.TypeMultiChoice); issue != "" {
				appendVisionReview(&report, question, issue)
				continue
			}
			if question.Type == models.TypeChoice && result.Score != 0 && result.Score != question.Score {
				appendVisionReview(&report, question, "单选题视觉评分必须是 0 分或满分")
				continue
			}
		}
		report.TotalScore += result.Score
		report.Results = append(report.Results, ScanGradeResult{QuestionID: question.ID, Number: segment.Number, Score: result.Score, MaxScore: question.Score, Status: result.Status, Note: firstNonEmpty(result.Note, "视觉 LLM 根据分割答题区域完成评分")})
	}
	for _, segment := range scanResult.Segments {
		known := false
		for _, question := range paper.Questions {
			if question.ID == segment.QuestionID {
				known = true
				break
			}
		}
		if !known && segment.QuestionID != "" {
			report.Warnings = append(report.Warnings, fmt.Sprintf("分割结果包含未知题目 %s", segment.QuestionID))
		}
	}
	if len(report.NeedsReview) > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d 道题需要人工复核", len(report.NeedsReview)))
	}
	return report
}

func gradeSegmentWithVision(ctx context.Context, client *llm.Client, model string, paper models.ExamPaper, question models.GeneratedQuestion, segment AnswerSegment) (visionGradeResponse, error) {
	data, err := os.ReadFile(segment.ImagePath)
	if err != nil {
		return visionGradeResponse{}, fmt.Errorf("读取分割图失败: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	rubric := rubricForQuestion(paper, question.ID)
	prompt := map[string]any{
		"questionId":     question.ID,
		"questionNumber": segment.Number,
		"questionType":   question.Type,
		"questionStem":   question.Stem,
		"options":        question.Options,
		"correctAnswer":  question.Answer,
		"maxScore":       question.Score,
		"rubric":         rubric,
		"segment":        segment,
		"instruction":    "只依据附图中的当前扫描答题区域判读。不要根据坐标猜测答案；看不清、涂写冲突、题号不确定或主观题证据不足时返回 needs_review。只返回 JSON。",
	}
	payload, err := json.Marshal(prompt)
	if err != nil {
		return visionGradeResponse{}, fmt.Errorf("构造视觉评分请求失败: %w", err)
	}
	messages := []llm.Message{
		{Role: "system", Content: "你是严格的扫描答题卡阅卷辅助模型。必须检查图片内容，只输出符合约定字段的 JSON，不得臆测。"},
		{Role: "user", Content: []llm.ContentPart{
			{Type: "text", Text: string(payload)},
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "data:image/png;base64," + encoded, Detail: "high"}},
		}},
	}
	var result visionGradeResponse
	if err := client.ChatJSON(ctx, model, messages, &result); err != nil {
		return visionGradeResponse{}, fmt.Errorf("视觉 LLM 请求失败: %w", err)
	}
	return result, nil
}

func rubricForQuestion(paper models.ExamPaper, questionID string) models.Rubric {
	for _, rubric := range paper.Rubrics {
		if rubric.QuestionID == questionID {
			return rubric
		}
	}
	return models.Rubric{QuestionID: questionID}
}

func appendVisionReview(report *ScanGradeReport, question models.GeneratedQuestion, note string) {
	for _, existing := range report.Results {
		if existing.QuestionID == question.ID {
			return
		}
	}
	report.Results = append(report.Results, ScanGradeResult{QuestionID: question.ID, MaxScore: question.Score, Status: StatusNeedsReview, Note: note})
	for _, id := range report.NeedsReview {
		if id == question.ID {
			return
		}
	}
	report.NeedsReview = append(report.NeedsReview, question.ID)
}

func validVisionScore(score, max float64) bool {
	return !math.IsNaN(score) && !math.IsInf(score, 0) && !math.IsNaN(max) && !math.IsInf(max, 0) && score >= 0 && score <= max
}

func validVisionConfidence(confidence float64) bool {
	return !math.IsNaN(confidence) && !math.IsInf(confidence, 0) && confidence >= 0.6 && confidence <= 1
}

func validateVisionOptions(options []string, multi bool) string {
	if len(options) == 0 {
		return "视觉模型未返回可验证的选项"
	}
	return optionLabelsIssue(options, multi)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
