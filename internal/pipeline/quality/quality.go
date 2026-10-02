// Package quality contains cheap, deterministic checks shared by generation,
// validation, assembly, and answer production. LLM output is untrusted input:
// it must pass these checks before it can become a deliverable.
package quality

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/choice"
)

var pollutionTokens = []string{
	"0verline", "0gester", "aso", "nec", "els", "conf", "udder",
	"异地恋", "固然2", "大自然0", "mythical", "canopy", "amaz$",
	"legendre", "kenny", "attitudes", "propagation", "z头脑发热", "resp:",
}

var bareAnswer = regexp.MustCompile(`^(?:答案|参考答案|answer|n/?a)[。.!！:：]?\s*$`)

var selfCorrectionTokens = []string{
	"题目有误", "条件之间存在矛盾", "修改题面", "重新审视",
	"假设题意", "如果题目要求", "不太对", "需手动补充",
}

// Issues returns deterministic quality failures for one generated question.
// An empty result means the question is suitable for the next stage.
func Issues(q models.GeneratedQuestion) []string {
	issues := make([]string, 0, 4)
	if blankOrPlaceholder(q.Stem) {
		issues = append(issues, "题面为空或为占位内容")
	}
	if blankOrPlaceholder(q.Answer) || bareAnswer.MatchString(strings.TrimSpace(q.Answer)) {
		issues = append(issues, "参考答案为空、占位或只有答案标签")
	}
	if polluted(q.Stem) {
		issues = append(issues, "题面包含疑似模型污染文本")
	}
	if polluted(q.Answer) {
		issues = append(issues, "参考答案包含疑似模型污染文本")
	}
	for _, token := range selfCorrectionTokens {
		if strings.Contains(q.Stem, token) || strings.Contains(q.Answer, token) {
			issues = append(issues, "题面或参考答案包含自我纠错文本")
			break
		}
	}
	if !finiteInRange(q.Difficulty, 0, 1) {
		issues = append(issues, "难度不在 0 到 1 范围内")
	}
	if q.Type == models.TypeChoice || q.Type == models.TypeMultiChoice {
		if len(q.Options) != 4 {
			issues = append(issues, fmt.Sprintf("选择题选项数量为 %d，期望 4", len(q.Options)))
		} else {
			seen := make(map[string]struct{}, 4)
			for i, option := range q.Options {
				if blankOrPlaceholder(option) || polluted(option) {
					issues = append(issues, fmt.Sprintf("第 %d 个选项为空或包含污染文本", i+1))
				}
				key := strings.TrimSpace(option)
				if _, ok := seen[key]; ok && key != "" {
					issues = append(issues, "选择题包含重复选项")
				}
				seen[key] = struct{}{}
			}
			labels := choice.Parse(q.Answer)
			if issue := choice.Validate(labels, q.Type == models.TypeMultiChoice); issue != "" {
				issues = append(issues, "选择题答案不是合法的 A-D 选项")
			}
		}
	}
	return unique(issues)
}

// SubjectContentIssues rejects generated math2 content that is outside its
// calculus/linear-algebra syllabus or has no recognisable syllabus point.
func SubjectContentIssues(subject string, points []string, content string) []string {
	if subject != "math2" && subject != "数学二" {
		return nil
	}
	blocked := []string{
		"概率论", "概率与统计", "概率密度", "概率分布", "二项分布", "随机变量",
		"数学期望", "方差", "协方差", "相关系数", "正态分布", "泊松分布",
		"抽样分布", "参数估计", "假设检验", "切比雪夫", "中心极限定理",
	}
	var issues []string
	for _, point := range points {
		for _, term := range blocked {
			if strings.Contains(point, term) {
				issues = append(issues, fmt.Sprintf("知识点 %q 超出数学二考纲", point))
				return unique(issues)
			}
		}
	}
	for _, term := range blocked {
		if strings.Contains(content, term) {
			issues = append(issues, fmt.Sprintf("题目内容涉及数学二不考的%s", term))
			break
		}
	}
	validPoint := false
	for _, point := range points {
		for _, marker := range []string{
			"极限", "连续", "导数", "微分", "积分", "函数", "方程", "级数",
			"矩阵", "行列式", "向量", "特征值", "二次型",
		} {
			if strings.Contains(point, marker) {
				validPoint = true
				break
			}
		}
		if validPoint {
			break
		}
	}
	if !validPoint {
		issues = append(issues, "数学二题目没有可识别的考纲知识点")
	}
	return unique(issues)
}

// PastQuestionInSubject filters out-of-syllabus source questions before they
// can influence composition, few-shot examples, or duplicate detection.
func PastQuestionInSubject(subject string, q models.PastQuestion) bool {
	if subject != "math2" && subject != "数学二" {
		return true
	}
	content := strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n")
	return len(SubjectContentIssues(subject, q.Points, content)) == 0
}

// Polluted reports whether text contains known model contamination markers.
func Polluted(text string) bool { return polluted(text) }

func polluted(text string) bool {
	lower := strings.ToLower(text)
	for _, token := range pollutionTokens {
		if strings.Contains(lower, strings.ToLower(token)) {
			return true
		}
	}
	return false
}

func blankOrPlaceholder(text string) bool {
	t := strings.TrimSpace(text)
	return t == "" || strings.HasPrefix(t, "%") || strings.Contains(strings.ToUpper(t), "TODO")
}

func finiteInRange(value, minValue, maxValue float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minValue && value <= maxValue
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
