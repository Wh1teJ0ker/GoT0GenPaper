package scan

import (
	"fmt"
	"math"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/choice"
)

var optionLabels = []string{"A", "B", "C", "D"}

type bubbleObservation struct {
	Label string
	Score float64
}

func bubbleInnerScore(g grayImage, h homography, x, y float64) float64 {
	// The template prints the option letter inside the circle. Sampling the
	// inner disk still captures pencil fill, but avoids most of the circle rim.
	return sampleCanonical(g, h, x, y, 2.9, 2.9)
}

// refineBubbleCenter follows the printed bubble locally instead of assuming
// that the page homography is exact everywhere. A scanner can introduce
// small non-linear warp, skew, or resampling drift after the four page marks
// have been fitted. The circular rim is stable under pencil filling, whereas
// nearby letters and table rules are not.
func refineBubbleCenter(g grayImage, h homography, x, y float64) (float64, float64) {
	bestX, bestY := x, y
	bestScore := bubbleShapeScore(g, h, x, y)
	for dy := -2.5; dy <= 2.5; dy += 0.5 {
		for dx := -2.5; dx <= 2.5; dx += 0.5 {
			candidate := bubbleShapeScore(g, h, x+dx, y+dy)
			if candidate > bestScore+0.01 {
				bestX, bestY, bestScore = x+dx, y+dy, candidate
			}
		}
	}
	return bestX, bestY
}

func bubbleShapeScore(g grayImage, h homography, x, y float64) float64 {
	// The generated bubble radius is about 3 pt. Use several points around
	// the rim and a quiet outer annulus. This score is only for local alignment;
	// answer classification still uses the inner darkness and its row baseline.
	const rimRadius = 3.2
	const outerRadius = 5.2
	rim := 0.0
	quiet := 0.0
	for i := 0; i < 16; i++ {
		angle := float64(i) * 2 * math.Pi / 16
		rim += canonicalDarkAt(g, h, x+rimRadius*math.Cos(angle), y+rimRadius*math.Sin(angle))
		quiet += canonicalDarkAt(g, h, x+outerRadius*math.Cos(angle), y+outerRadius*math.Sin(angle))
	}
	rim /= 16
	quiet /= 16
	return 0.8*rim + 0.2*(1-quiet)
}

func classifyBubbles(obs []bubbleObservation, multi bool) ([]string, float64, string, string) {
	if len(obs) == 0 {
		return nil, 0, StatusNeedsReview, "没有可识别的选项区域"
	}
	values := make([]float64, len(obs))
	for i := range obs {
		values[i] = obs[i].Score
	}
	maxScore := 0.0
	for _, v := range values {
		if v > maxScore {
			maxScore = v
		}
	}
	// The baseline printed glyph differs slightly between A/B/C/D. A mark is
	// accepted only when it is both dark enough and separates from the local
	// option baseline. Weak/close candidates are deliberately ambiguous.
	// Estimate the unmarked background from the lower half. Using the plain
	// median would let two dark marks raise the threshold and hide a multi-mark
	// answer exactly when we need to flag it.
	sorted := append([]float64(nil), values...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	background := sorted[(len(sorted)-1)/2]
	baseline := background
	threshold := math.Max(0.34, baseline+0.12)
	selected := make([]string, 0, len(obs))
	for _, o := range obs {
		if o.Score >= threshold && o.Score >= 0.40 {
			selected = append(selected, o.Label)
		}
	}
	if len(selected) == 0 {
		if maxScore < 0.34 {
			return nil, clamp(1-maxScore/0.34, 0, 1), StatusBlank, "未检测到有效涂写"
		}
		return nil, 0.35, StatusAmbiguous, "存在疑似涂写但黑度不足"
	}
	if !multi && len(selected) > 1 {
		return selected, confidenceFor(values, selected), StatusAmbiguous, "单选题检测到多个涂写项"
	}
	if len(selected) > 1 && confidenceFor(values, selected) < 0.25 {
		return selected, confidenceFor(values, selected), StatusAmbiguous, "多个选项黑度接近"
	}
	return selected, confidenceFor(values, selected), StatusRecognized, ""
}

func confidenceFor(values []float64, selected []string) float64 {
	if len(selected) == 0 || len(values) == 0 {
		return 0
	}
	maxScore := 0.0
	second := 0.0
	for _, v := range values {
		if v > maxScore {
			second = maxScore
			maxScore = v
		} else if v > second {
			second = v
		}
	}
	return clamp(0.35+2*(maxScore-second), 0, 1)
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	for i := 1; i < len(copyValues); i++ {
		for j := i; j > 0 && copyValues[j] < copyValues[j-1]; j-- {
			copyValues[j], copyValues[j-1] = copyValues[j-1], copyValues[j]
		}
	}
	return copyValues[len(copyValues)/2]
}

func scanObjectivePage(g grayImage, h homography, questions []models.GeneratedQuestion) ([]ObjectiveAnswer, bool, float64) {
	layout, layoutOK := detectChoiceLayout(g, h, len(questions))
	return scanObjectivePageWithLayout(g, h, questions, layout, layoutOK)
}

func scanObjectivePageWithLayout(g grayImage, h homography, questions []models.GeneratedQuestion, layout choiceLayout, layoutOK bool) ([]ObjectiveAnswer, bool, float64) {
	answers := make([]ObjectiveAnswer, 0, len(questions))
	if !layoutOK {
		for slot, q := range questions {
			answers = append(answers, ObjectiveAnswer{
				QuestionID: q.ID,
				Number:     slot + 1,
				Selected:   []string{},
				Status:     StatusNeedsReview,
				Note:       "未从扫描图恢复完整题目行列结构，未执行自动涂卡识别",
			})
		}
		return answers, false, 0
	}
	for slot, q := range questions {
		observations := make([]bubbleObservation, 0, len(optionLabels))
		for option, label := range optionLabels {
			center := layout.Centers[slot*len(optionLabels)+option]
			x, y := refineBubbleCenter(g, h, center.X, center.Y)
			observations = append(observations, bubbleObservation{Label: label, Score: bubbleInnerScore(g, h, x, y)})
		}
		multi := q.Type == models.TypeMultiChoice
		selected, confidence, status, note := classifyBubbles(observations, multi)
		if selected == nil {
			selected = []string{}
		}
		answers = append(answers, ObjectiveAnswer{QuestionID: q.ID, Number: slot + 1, Selected: selected, Confidence: confidence, Status: status, Note: note})
	}
	return answers, true, layout.Confidence
}

func isObjective(q models.GeneratedQuestion) bool {
	return q.Type == models.TypeChoice || q.Type == models.TypeMultiChoice
}

func objectiveAnswerKey(q models.GeneratedQuestion) []string {
	return choice.Parse(q.Answer)
}

func sameOptions(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func objectiveGrade(q models.GeneratedQuestion, answer ObjectiveAnswer) (float64, string, string) {
	score := q.Score
	if !validQuestionScore(score) {
		return 0, StatusNeedsReview, "题目分值无效"
	}
	if answer.Status == StatusBlank {
		return 0, StatusBlank, "空白答案"
	}
	if answer.Status != StatusRecognized {
		return 0, StatusNeedsReview, fmt.Sprintf("选择题识别状态为 %s", answer.Status)
	}
	if math.IsNaN(answer.Confidence) || math.IsInf(answer.Confidence, 0) || answer.Confidence < 0.5 || answer.Confidence > 1 {
		return 0, StatusNeedsReview, fmt.Sprintf("客观题识别置信度过低(%.2f)", answer.Confidence)
	}
	if issue := optionLabelsIssue(answer.Selected, q.Type == models.TypeMultiChoice); issue != "" {
		return 0, StatusNeedsReview, issue
	}
	key := objectiveAnswerKey(q)
	if issue := optionLabelsIssue(key, q.Type == models.TypeMultiChoice); issue != "" {
		if len(key) == 0 {
			return 0, StatusNeedsReview, "试卷缺少可解析的标准答案"
		}
		return 0, StatusNeedsReview, "试卷标准答案选项数量或格式无效"
	}
	if sameOptions(key, answer.Selected) {
		return score, StatusRecognized, "客观题答案完全匹配"
	}
	return 0, StatusRecognized, "客观题答案与标准答案不匹配"
}

func optionLabelsIssue(labels []string, multi bool) string {
	return choice.Validate(labels, multi)
}
