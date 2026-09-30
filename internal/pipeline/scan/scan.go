package scan

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"GoT0GenPaper/internal/models"
)

// Run scans answer-sheet images. The first page contains OMR choices; later
// pages are subjective answer areas. Every page must be calibrated from its
// own registration marks, so scans may be resized, cropped, rotated, or mildly
// perspective-distorted without relying on raw pixel coordinates.
func Run(ctx context.Context, paper models.ExamPaper, imagePaths []string, options ScanOptions) (*ScanResult, error) {
	if len(imagePaths) == 0 {
		return nil, fmt.Errorf("至少需要一张答题卡扫描图")
	}
	objective := objectiveQuestions(paper)
	subjective := make([]models.GeneratedQuestion, 0)
	for _, q := range paper.Questions {
		if !isObjective(q) {
			subjective = append(subjective, q)
		}
	}
	result := &ScanResult{
		PaperID:          paper.ID,
		PaperFingerprint: paperFingerprint(paper),
		Subject:          paper.Subject,
		Pages:            make([]PageScanResult, 0, len(imagePaths)),
		Objective:        make([]ObjectiveAnswer, 0, len(objective)),
		Subjective:       make([]SubjectiveAnswer, 0, len(subjective)),
		Warnings:         make([]string, 0),
	}
	segmentsDir := options.SegmentsDir
	if segmentsDir == "" {
		var err error
		segmentsDir, err = os.MkdirTemp("", "got0genpaper-segments-")
		if err != nil {
			return nil, fmt.Errorf("创建答题区域分割目录: %w", err)
		}
	}
	if err := os.MkdirAll(segmentsDir, 0755); err != nil {
		return nil, fmt.Errorf("创建答题区域分割目录 %s: %w", segmentsDir, err)
	}
	objectivePages := objectivePageCount(len(objective))
	pageGrays := make([]grayImage, 0, len(imagePaths))
	pageTransforms := make([]homography, 0, len(imagePaths))
	pageCalibrated := make([]bool, 0, len(imagePaths))
	for i, path := range imagePaths {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("扫描图不存在 %s: %w", path, err)
		}
		page, err := loadPage(path)
		if err != nil {
			return nil, err
		}
		gray := toGray(page.Image)
		markers := findMarkers(gray)
		cornerCandidates := registrationCornerCandidates(markers, page.Width, page.Height)
		pageQuestions := objectiveQuestionsForPage(objective, i)
		h := identityHomography()
		calibrated := false
		if len(cornerCandidates) > 0 {
			// Try the geometry hypothesis that explains the current choice grid.
			// The default orientation is normally first; rotated scans only pay
			// for more hypotheses after that fast path fails.
			for candidateIndex, corners := range cornerCandidates {
				fitted, fitErr := fitHomography(corners)
				if fitErr != nil {
					continue
				}
				if len(pageQuestions) == 0 || candidateIndex == 0 {
					h, calibrated = fitted, true
					if len(objective) == 0 {
						break
					}
					// Keep the default candidate as a fallback, but prefer a
					// candidate whose measured grid is complete.
					if _, found, _ := scanObjectivePage(gray, fitted, pageQuestions); found {
						break
					}
					continue
				}
				if _, found, _ := scanObjectivePage(gray, fitted, pageQuestions); found {
					h, calibrated = fitted, true
					break
				}
			}
		}
		status, warnings := pageStatus(calibrated, len(markers))
		if !calibrated {
			warnings = append(warnings, "未完成几何校准，本页不执行自动识别")
			if options.AllowUncalibrated {
				warnings = append(warnings, "未校准诊断模式只保留页面信息，不使用原始坐标自动判分")
			} else {
				warnings = append(warnings, "未启用未校准诊断模式")
			}
		}
		result.Pages = append(result.Pages, PageScanResult{Page: i + 1, Path: path, Width: page.Width, Height: page.Height, MarkerCount: len(markers), Calibrated: calibrated, Status: status, Warnings: warnings})
		pageGrays = append(pageGrays, gray)
		pageTransforms = append(pageTransforms, h)
		pageCalibrated = append(pageCalibrated, calibrated)
	}
	for page := 0; page < objectivePages; page++ {
		const choicePageSize = 40
		start := page * choicePageSize
		end := start + choicePageSize
		if end > len(objective) {
			end = len(objective)
		}
		if page < len(pageGrays) && pageCalibrated[page] {
			layout, layoutFound := detectChoiceLayout(pageGrays[page], pageTransforms[page], end-start)
			answers, layoutFound, layoutScore := scanObjectivePageWithLayout(pageGrays[page], pageTransforms[page], objective[start:end], layout, layoutFound)
			result.Pages[page].LayoutFound = layoutFound
			result.Pages[page].LayoutScore = layoutScore
			if !layoutFound {
				result.Pages[page].Warnings = append(result.Pages[page].Warnings, "未恢复完整题目行列结构，本页客观题全部转人工复核")
				result.Pages[page].Status = StatusNeedsReview
			}
			for i := range answers {
				answers[i].Number = start + i + 1
			}
			result.Objective = append(result.Objective, answers...)
			segments, segmentErr := buildObjectiveSegments(pageGrays[page], pageTransforms[page], imagePaths[page], segmentsDir, page, start, objective[start:end], layout)
			if segmentErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("第 %d 张客观题页分割失败: %v", page+1, segmentErr))
			} else {
				result.Segments = append(result.Segments, segments...)
			}
		} else {
			for slot, q := range objective[start:end] {
				result.Objective = append(result.Objective, ObjectiveAnswer{QuestionID: q.ID, Number: start + slot + 1, Status: StatusNeedsReview, Note: "客观题页未完成几何校准"})
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("第 %d 张客观题页无法完成定位标记校准", page+1))
		}
	}
	pageOffset := objectivePages
	if pageOffset < 1 {
		// The first sheet page is always the candidate-information page, even
		// when a paper has no objective section.
		pageOffset = 1
	}
	result.Subjective = pendingVisionAnswers(imagePaths, subjective, pageOffset)
	for i, question := range subjective {
		page := pageOffset + i
		if page >= len(pageGrays) || page >= len(pageTransforms) || !pageCalibrated[page] {
			result.Segments = append(result.Segments, AnswerSegment{
				ID:         fmt.Sprintf("p%d-q%d", page+1, i+1),
				QuestionID: question.ID,
				Number:     i + 1,
				Page:       page + 1,
				Kind:       "subjective",
				SourcePath: imagePaths[minInt(page, len(imagePaths)-1)],
				Status:     StatusNeedsReview,
				Note:       "主观题页未完成几何校准，未生成可靠分割区域",
			})
			continue
		}
		segment, segmentErr := buildSubjectiveSegments(pageGrays[page], pageTransforms[page], imagePaths[page], segmentsDir, page, i+1, question.ID)
		if segmentErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("主观题 %s 分割失败: %v", question.ID, segmentErr))
			continue
		}
		result.Segments = append(result.Segments, segment)
	}
	for _, p := range result.Pages {
		result.Warnings = append(result.Warnings, p.Warnings...)
	}
	return result, nil
}

func pendingVisionAnswers(paths []string, questions []models.GeneratedQuestion, pageOffset int) []SubjectiveAnswer {
	answers := make([]SubjectiveAnswer, 0, len(questions))
	for i, question := range questions {
		page := pageOffset + i
		answer := SubjectiveAnswer{
			QuestionID:   question.ID,
			Number:       i + 1,
			Page:         page + 1,
			VisionStatus: "pending",
			Status:       StatusNeedsReview,
			Note:         "等待多模态模型读取当前扫描答题区域",
		}
		if page >= len(paths) {
			answer.VisionStatus = "missing_image"
			answer.Note = "缺少对应答题页扫描图"
		}
		answers = append(answers, answer)
	}
	return answers
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func objectiveQuestionsForPage(objective []models.GeneratedQuestion, page int) []models.GeneratedQuestion {
	const pageSize = 40
	start := page * pageSize
	if start >= len(objective) {
		return nil
	}
	end := start + pageSize
	if end > len(objective) {
		end = len(objective)
	}
	return objective[start:end]
}

// GradeScan applies deterministic scoring to locally recognized objective
// answers. Production scan grading should use GradeScanWithVision so both
// objective and subjective answer regions are judged from the current image.
func GradeScan(paper models.ExamPaper, scanResult ScanResult) ScanGradeReport {
	report := ScanGradeReport{Scan: scanResult, Results: make([]ScanGradeResult, 0), NeedsReview: make([]string, 0), Warnings: make([]string, 0)}
	reviewed := make(map[string]struct{})
	addReview := func(id string) {
		if id == "" {
			return
		}
		if _, exists := reviewed[id]; exists {
			return
		}
		reviewed[id] = struct{}{}
		report.NeedsReview = append(report.NeedsReview, id)
	}
	objective := objectiveQuestions(paper)
	knownObjective := make(map[string]struct{}, len(objective))
	duplicateObjective := make(map[string]bool)
	for _, q := range objective {
		if _, exists := knownObjective[q.ID]; exists {
			duplicateObjective[q.ID] = true
		}
		knownObjective[q.ID] = struct{}{}
	}
	objectiveReady := objectiveScanReady(scanResult, len(objective))
	paperMatch := scanResult.PaperID == "" || paper.ID == "" || scanResult.PaperID == paper.ID
	if expectedFingerprint := paperFingerprint(paper); expectedFingerprint != "" {
		paperMatch = paperMatch && scanResult.PaperFingerprint == expectedFingerprint
	}
	if len(objective) > 0 && !objectiveReady {
		report.Warnings = append(report.Warnings, "客观题扫描页未全部完成定位和版面校准，客观题不自动计分")
	}
	if !paperMatch {
		report.Warnings = append(report.Warnings, "扫描结果所属试卷或版面指纹与当前试卷不一致，客观题不自动计分")
	}
	for _, q := range paper.Questions {
		if !validQuestionScore(q.Score) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("题目 %s 的分值无效，无法计入总分", q.ID))
			addReview(q.ID)
			continue
		}
		report.MaxScore += q.Score
	}
	answersByID := make(map[string]ObjectiveAnswer, len(scanResult.Objective))
	duplicateAnswers := make(map[string]bool)
	for _, answer := range scanResult.Objective {
		if answer.QuestionID == "" {
			report.Warnings = append(report.Warnings, "扫描结果包含缺少题号的客观题，已转人工复核")
			continue
		}
		if _, exists := answersByID[answer.QuestionID]; exists {
			duplicateAnswers[answer.QuestionID] = true
			continue
		}
		answersByID[answer.QuestionID] = answer
	}
	for i, q := range objective {
		maxScore := q.Score
		if !validQuestionScore(maxScore) {
			maxScore = 0
		}
		answer, exists := answersByID[q.ID]
		if duplicateObjective[q.ID] {
			addReview(q.ID)
			report.Results = append(report.Results, ScanGradeResult{QuestionID: q.ID, Number: i + 1, MaxScore: maxScore, Status: StatusNeedsReview, Note: "试卷包含重复客观题 ID"})
			continue
		}
		if !exists || duplicateAnswers[q.ID] {
			addReview(q.ID)
			note := "缺少客观题扫描结果"
			if duplicateAnswers[q.ID] {
				note = "同一道客观题存在多个扫描结果"
			}
			report.Results = append(report.Results, ScanGradeResult{QuestionID: q.ID, Number: i + 1, MaxScore: maxScore, Status: StatusNeedsReview, Note: note})
			continue
		}
		if !objectiveReady || !paperMatch {
			addReview(q.ID)
			report.Results = append(report.Results, ScanGradeResult{QuestionID: q.ID, Number: i + 1, MaxScore: maxScore, Status: StatusNeedsReview, Note: "扫描页未完成可靠校准，未自动计分"})
			continue
		}
		if !validQuestionScore(q.Score) {
			report.Results = append(report.Results, ScanGradeResult{QuestionID: q.ID, Number: i + 1, MaxScore: 0, Status: StatusNeedsReview, Note: "题目分值无效，未自动计分"})
			continue
		}
		score, status, note := objectiveGrade(q, answer)
		report.TotalScore += score
		if status == StatusNeedsReview || answer.Status == StatusAmbiguous {
			addReview(q.ID)
		}
		report.Results = append(report.Results, ScanGradeResult{QuestionID: q.ID, Number: i + 1, Score: score, MaxScore: maxScore, Status: status, Note: note})
	}
	for _, answer := range scanResult.Objective {
		if _, known := knownObjective[answer.QuestionID]; !known {
			if answer.QuestionID == "" {
				continue
			}
			addReview(answer.QuestionID)
			report.Warnings = append(report.Warnings, fmt.Sprintf("扫描结果包含未知客观题 %s", answer.QuestionID))
		}
	}
	subjectiveQuestions := make([]models.GeneratedQuestion, 0)
	knownSubjective := make(map[string]struct{})
	seenSubjective := make(map[string]bool)
	for _, q := range paper.Questions {
		if !isObjective(q) {
			subjectiveQuestions = append(subjectiveQuestions, q)
			knownSubjective[q.ID] = struct{}{}
		}
	}
	for _, answer := range scanResult.Subjective {
		id := answer.QuestionID
		if id == "" {
			report.Warnings = append(report.Warnings, "扫描结果包含缺少题号的主观题，已转人工复核")
			report.Results = append(report.Results, ScanGradeResult{QuestionID: id, Number: answer.Number, Status: StatusNeedsReview, Note: "主观题扫描结果缺少题号"})
			continue
		}
		if _, known := knownSubjective[id]; !known {
			addReview(id)
			report.Warnings = append(report.Warnings, fmt.Sprintf("扫描结果包含未知主观题 %s", id))
			report.Results = append(report.Results, ScanGradeResult{QuestionID: id, Number: answer.Number, Status: StatusNeedsReview, Note: "扫描结果包含未知主观题"})
			continue
		}
		addReview(id)
		note := "主观题必须人工阅卷"
		if seenSubjective[id] {
			note = "同一道主观题存在多个扫描结果，必须人工复核"
			report.Warnings = append(report.Warnings, fmt.Sprintf("主观题 %s 存在重复扫描结果", id))
		}
		seenSubjective[id] = true
		report.Results = append(report.Results, ScanGradeResult{QuestionID: id, Number: answer.Number, MaxScore: questionScore(paper, id), Status: StatusNeedsReview, Note: note})
	}
	for _, q := range subjectiveQuestions {
		if !seenSubjective[q.ID] {
			addReview(q.ID)
			report.Warnings = append(report.Warnings, fmt.Sprintf("缺少主观题 %s 的扫描结果", q.ID))
			report.Results = append(report.Results, ScanGradeResult{QuestionID: q.ID, MaxScore: questionScore(paper, q.ID), Status: StatusNeedsReview, Note: "缺少主观题扫描结果"})
		}
	}
	if len(report.NeedsReview) > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d 道题需要人工复核", len(report.NeedsReview)))
	}
	return report
}

// paperFingerprint binds a scan to the question order and answer-sheet
// layout, while avoiding answer text and other mutable content. An empty paper
// ID is treated as an ad-hoc unit-test/legacy paper and keeps compatibility
// with manually constructed ScanResult values.
func paperFingerprint(paper models.ExamPaper) string {
	if strings.TrimSpace(paper.ID) == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(paper.ID))
	h.Write([]byte{0})
	h.Write([]byte(paper.Subject))
	for _, q := range paper.Questions {
		h.Write([]byte{0})
		h.Write([]byte(q.ID))
		h.Write([]byte{0})
		h.Write([]byte(q.SpecID))
		h.Write([]byte{0})
		h.Write([]byte(q.Type))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatFloat(q.Score, 'f', 6, 64)))
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

func validQuestionScore(score float64) bool {
	return score > 0 && !math.IsNaN(score) && !math.IsInf(score, 0)
}

func objectivePageCount(questionCount int) int {
	if questionCount <= 0 {
		return 0
	}
	const choicePageSize = 40
	return (questionCount + choicePageSize - 1) / choicePageSize
}

func objectiveScanReady(result ScanResult, questionCount int) bool {
	for page := 0; page < objectivePageCount(questionCount); page++ {
		if page >= len(result.Pages) || !result.Pages[page].Calibrated || !result.Pages[page].LayoutFound {
			return false
		}
	}
	return true
}

func questionScore(paper models.ExamPaper, id string) float64 {
	for _, q := range paper.Questions {
		if q.ID == id {
			if !validQuestionScore(q.Score) {
				return 0
			}
			return q.Score
		}
	}
	return 0
}
