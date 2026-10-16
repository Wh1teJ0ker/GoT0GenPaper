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
	"0verline", "0gester", "异地恋", "固然2", "大自然0", "amaz$", "z头脑发热", "resp:",
}

var shortPollutionToken = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:aso|nec|els|conf)(?:$|[^a-z])`)

var bareAnswer = regexp.MustCompile(`^(?:答案|参考答案|answer|n/?a)[。.!！:：]?\s*$`)
var englishBlankPattern = regexp.MustCompile(`(?i)(?:\\(?:underline|fillin)\s*\{[^{}]*\}|_{3,}|\[\s*(?:blank\s*\d*|x|question)\s*\])`)
var englishMetaPromptPattern = regexp.MustCompile(`(?i)(?:the passage contains a context|this question tests|this item examines|the question is designed to test)`)
var englishPlaceholderPattern = regexp.MustCompile(`(?i)(?:\[\s*(?:x|question)\s*\]|\\text(?:bf|it)?\s*\{\s*question\s+\d+\s*\})`)
var englishContextReferencePattern = regexp.MustCompile(`(?i)\b(?:paragraph\s+\d+|based on the passage|according to the passage|from paragraph\s+\d+|the passage suggests)\b`)
var sequenceConclusionPattern = regexp.MustCompile(`(?i)(?:(BFS|DFS)[^。\n]{0,16})?(?:访问)?序列[^。\n]{0,8}?(?:为|是|:|：)\s*((?:\d+\s*[,，、]\s*){3,}\d+)`)
var numericSequencePattern = regexp.MustCompile(`\d+(?:\s*[,，、]\s*\d+){3,}`)

// optionMentionPattern accepts the label styles that the Chinese and English
// generation prompts permit. Keep the label itself in a capture group so the
// same check can be used for either language.
var optionMentionPattern = regexp.MustCompile(`(?i)(?:\b(?:选项|option|choice)\s*[:：]?\s*\(?([A-D])\)?(?:\s|[.．:：、)）]|$)|(?:^|[^a-z0-9])\(([A-D])\)(?:\s|$)|(?:^|[^a-z0-9])\(?([A-D])\)?\s*(?:选项|项|[.．、)）:：]))`)
var visitedOnlyCycleDetectionPattern = regexp.MustCompile(`(?i)(?:能够|可以|可)\s*(?:通过|根据)\s*[^。\n]{0,40}visited[^。\n]{0,20}(?:判断|检测|识别)[^。\n]{0,8}(?:有向)?(?:环|回路)`)
var infiniteSeriesNotation = regexp.MustCompile(`(?is)(?:\\sum|∑)\s*(?:\\limits|\\nolimits)?\s*_\s*\{[^}]{0,100}\}\s*\^\s*\{?(?:\\infty|∞)`)
var politicsMathNotation = regexp.MustCompile(`(?i)\\(?:int|sum|lim|frac|partial|sqrt|begin\{(?:matrix|aligned|cases)\})|\b(?:f|g|y)\s*\(\s*x\s*\)\s*(?:=|')`)

var politicsModules = map[string]struct{}{
	"马克思主义基本原理":            {},
	"毛泽东思想和中国特色社会主义理论体系概论": {},
	"习近平新时代中国特色社会主义思想概论":   {},
	"中国近现代史纲要":             {},
	"思想道德与法治":              {},
	"形势与政策以及当代世界经济与政治":     {},
}

var politicsOutOfScopeTokens = []string{
	"导数", "微分方程", "微积分", "微分", "不定积分", "定积分", "矩阵", "行列式", "特征值",
	"随机变量", "概率密度", "概率论", "正态分布", "方差", "协方差", "数列敛散", "幂级数", "无穷级数",
	"数据结构", "操作系统", "计算机组成原理", "算法复杂度", "图的遍历", "TCP/IP", "完形填空", "阅读理解",
}

var selfCorrectionTokens = []string{
	"题目有误", "条件之间存在矛盾", "修改题面", "重新审视",
	"假设题意", "如果题目要求", "不太对", "需手动补充",
	"原选项", "修正题面", "重新设计", "重新校对", "未在选项中", "不在选项中",
	"假设题目意图", "取决于具体的拓扑结构", "需根据拓扑图具体判断",
	"重新审查", "重新检查", "再次检查", "重新核对", "选项中无", "选项中没有",
	"无对应正确答案", "正确答案应", "推导过程有误", "修改后的选项", "鬼地方", "注：删去",
}

var explanationSignals = []string{
	"因为", "由于", "根据", "由", "令", "设", "代入", "计算", "推得", "可得", "故", "因此", "所以", "证明", "解得", "说明", "即",
	"考查", "根据题意", "模拟", "遍历", "对照选项", "对应", "由此", "因而", "语境", "搭配", "词义", "句意", "符合", "不符合", "不符", "排除", "上下文",
	"because", "since", "therefore", "thus", "hence", "let", "given", "substitute", "calculate", "which means",
}

// englishExplanationSignals supplements the shared list for English papers. An
// English answer can legitimately explain in English (means/explains/states) or
// in Chinese (译文/要点/指代), and translation items discuss clauses and voice
// rather than mathematical derivation, so the vocabulary is wider here.
var englishExplanationSignals = []string{
	"context", "grammar", "collocation", "meaning", "means", "fits", "match", "matches", "suggests", "indicates",
	"refers to", "supports", "whereas", "unlike", "appropriate", "inappropriate", "consistent with", "in contrast",
	"explains", "correct", "incorrect", "wrong", "option", "states", "mentions", "according to", "tone",
	"structure", "clause", "voice", "purpose", "logical", "paragraph",
	"翻译", "译文", "要点", "句意", "语法", "衔接", "逻辑", "指代", "语义", "意思", "正确", "错误", "原文",
	"上文", "下文", "上下文", "主语", "谓语", "从句", "语序", "符合", "不符", "排除", "搭配",
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
	issues = append(issues, ExplanationIssues(q)...)
	issues = append(issues, ChoiceConclusionIssues(q)...)
	issues = append(issues, EnglishQuestionIssues(q)...)
	if is408Question(q) {
		issues = append(issues, DataStructure408Issues(strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n"))...)
		issues = append(issues, AddressCalculationIssues(q)...)
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
		wantOptions := ExpectedChoiceOptionCount(q)
		if len(q.Options) != wantOptions {
			issues = append(issues, fmt.Sprintf("选择题选项数量为 %d，期望 %d", len(q.Options), wantOptions))
		} else {
			seen := make(map[string]struct{}, wantOptions)
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
			alphabet := "ABCD"
			if models.IsEnglishPartB(q.Points) {
				alphabet = "ABCDEFG"
				labels = choice.ParseWithAlphabet(q.Answer, alphabet)
			}
			if issue := choice.ValidateWithAlphabet(labels, q.Type == models.TypeMultiChoice, alphabet); issue != "" {
				issues = append(issues, "选择题答案不在有效选项范围内")
			}
			if !models.IsEnglishPartB(q.Points) {
				issues = append(issues, ChoiceConsistencyIssues(q)...)
			}
		}
	}
	return unique(issues)
}

// ChoiceConclusionIssues rejects answers whose explicit final-answer markers
// disagree with the answer token at the beginning.  Models often calculate
// one option correctly and then append a stale "选择 X" sentence; accepting
// that text makes both answer keys and explanations unsafe.
func ChoiceConclusionIssues(q models.GeneratedQuestion) []string {
	if q.Type != models.TypeChoice && q.Type != models.TypeMultiChoice {
		return nil
	}
	alphabet := "ABCD"
	if models.IsEnglishPartB(q.Points) {
		alphabet = "ABCDEFG"
	}
	initial := choice.ParseWithAlphabet(q.Answer, alphabet)
	if len(initial) != 1 || strings.TrimSpace(q.Answer) == "" {
		return nil
	}
	// Only inspect phrases that express a conclusion.  "A 项错误，B 项
	// 正确" in the distractor analysis is not a final-answer marker.
	finalMarker := regexp.MustCompile(`(?i)(?:因此|所以|故|故而|从而|综上|结论|最终(?:答案|选择)?|正确答案|选择)\s*(?:为|是|：|:)??\s*(?:选项)?\s*([` + alphabet + `])\s*(?:项|选项)?\s*(?:正确|错误|对|错)?(?:[。.!！,，;；]|$)`)
	var issues []string
	for _, match := range finalMarker.FindAllStringSubmatch(q.Answer, -1) {
		if len(match) < 2 || strings.EqualFold(match[1], initial[0]) {
			continue
		}
		issues = append(issues, fmt.Sprintf("答案首字母为 %s，但结论句给出 %s", initial[0], strings.ToUpper(match[1])))
		break
	}
	return issues
}

// AddressCalculationIssues contains a small, deterministic guard for the
// high-risk address-partitioning pattern used by the 408 corpus.  It catches
// the old cached item before it can be delivered again: the virtual-page TLB
// index comes from the virtual page number, the physical Cache index comes
// from the physical address, and a 30-bit physical address must fit below
// 0x40000000.
func AddressCalculationIssues(q models.GeneratedQuestion) []string {
	if q.SpecID != "spec_020" || !strings.Contains(q.Stem, "0x12345678") {
		return nil
	}
	if strings.Contains(q.Stem, "0xABCDEF00") {
		return []string{"30位物理地址不能取 0xABCDEF00；该地址超出 30 位地址空间"}
	}
	if strings.Contains(q.Answer, "TLB索引为 0x04") || strings.Contains(q.Answer, "Cache索引为 0x1F0") {
		return []string{"地址划分计算不一致：TLB 索引应取虚拟页号低 4 位，Cache 索引应取物理地址低 11 位字段"}
	}
	return nil
}

// ExpectedChoiceOptionCount returns the option count for a generated
// objective question. English II Part B uses seven shared headings (A-G).
func ExpectedChoiceOptionCount(q models.GeneratedQuestion) int {
	if models.IsEnglishPartB(q.Points) {
		return 7
	}
	return 4
}

// EnglishQuestionIssues catches malformed single-item English objective
// questions, especially cloze passages with multiple unanswered blanks but
// only one four-option answer set.
func EnglishQuestionIssues(q models.GeneratedQuestion) []string {
	if q.Type != models.TypeChoice && q.Type != models.TypeMultiChoice {
		return nil
	}
	english := false
	for _, point := range q.Points {
		normalized := strings.ToLower(strings.TrimSpace(point))
		if strings.Contains(normalized, "英语一") || strings.Contains(normalized, "英语二") ||
			strings.Contains(normalized, "english1") || strings.Contains(normalized, "english2") {
			english = true
			break
		}
	}
	if !english {
		return nil
	}
	var issues []string
	if blanks := len(englishBlankPattern.FindAllStringIndex(q.Stem, -1)); blanks > 1 {
		issues = append(issues, fmt.Sprintf("英语客观题包含 %d 个待答空，但仅提供一组选择项", blanks))
	}
	if englishMetaPromptPattern.MatchString(q.Stem) {
		issues = append(issues, "英语题干包含命题过程元叙述，不像正式试题文本")
	}
	if englishPlaceholderPattern.MatchString(q.Stem) {
		issues = append(issues, "英语题干包含未替换的题目占位符")
	}
	if strings.TrimSpace(q.SharedMaterial) == "" && englishContextReferencePattern.MatchString(q.Stem) && len([]rune(strings.TrimSpace(q.Stem))) < 320 {
		issues = append(issues, "英语阅读题引用文章或段落但未提供可独立作答的文章材料")
	}
	return issues
}

func is408Question(q models.GeneratedQuestion) bool {
	for _, point := range q.Points {
		normalized := strings.ToLower(strings.TrimSpace(point))
		if normalized == "408" || strings.Contains(normalized, "计算机学科专业基础") {
			return true
		}
	}
	return false
}

// DataStructure408Issues catches a high-impact misconception that boolean
// visited state alone cannot distinguish a back edge from other visited-edge
// kinds in a directed graph. A recursion-stack/color state is required.
func DataStructure408Issues(content string) []string {
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "有向") || !strings.Contains(lower, "visited") ||
		!visitedOnlyCycleDetectionPattern.MatchString(content) {
		return nil
	}
	if strings.Contains(content, "递归栈") || strings.Contains(content, "三色") ||
		strings.Contains(lower, "color") || strings.Contains(content, "颜色标记") {
		return nil
	}
	return []string{"有向图环检测不能仅依据 visited 标志；需用递归栈或颜色状态区分回边"}
}

// ChoiceConsistencyIssues catches high-impact choice errors with deterministic
// checks: options copied into the stem, a conclusion that reproduces an
// unselected option, and traversal sequences that correspond only to
// unselected options.
func ChoiceConsistencyIssues(q models.GeneratedQuestion) []string {
	if q.Type != models.TypeChoice && q.Type != models.TypeMultiChoice || len(q.Options) != 4 {
		return nil
	}
	labels := choice.Parse(q.Answer)
	if choice.Validate(labels, q.Type == models.TypeMultiChoice) != "" {
		return nil
	}
	selected := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		selected[strings.ToUpper(label)] = struct{}{}
	}
	stemText := comparableText(q.Stem)
	conclusionText := choiceConclusionText(q.Answer)
	stemMatches := 0
	for index, option := range q.Options {
		body := comparableText(option)
		if len([]rune(body)) < 8 {
			continue
		}
		if strings.Contains(stemText, body) {
			stemMatches++
		}
		label := string(rune('A' + index))
		if _, isSelected := selected[label]; isSelected || !strings.Contains(conclusionText, body) {
			continue
		}
		if mentionsOptionLabel(q.Answer, label) {
			continue
		}
		return []string{fmt.Sprintf("解析推导与最终答案可能不一致：包含未选项 %s 的完整内容", label)}
	}
	if issue := derivedSequenceIssue(q.Answer, q.Options, selected); issue != "" {
		return []string{issue}
	}
	if stemMatches >= 2 {
		return []string{"选择题题面重复列出选项，选项应只放在 options 字段"}
	}
	return nil
}

func derivedSequenceIssue(answer string, options []string, selected map[string]struct{}) string {
	for _, match := range sequenceConclusionPattern.FindAllStringSubmatch(answer, -1) {
		kind := strings.ToUpper(match[1])
		sequence := normalizeSequence(match[2])
		matchedOptions := make([]string, 0, 1)
		for index, option := range options {
			for _, location := range numericSequencePattern.FindAllStringIndex(option, -1) {
				candidate := option[location[0]:location[1]]
				if normalizeSequence(candidate) == sequence {
					if kind != "" && sequenceKindAt(option, location[0]) != kind {
						continue
					}
					matchedOptions = append(matchedOptions, string(rune('A'+index)))
					break
				}
			}
		}
		if len(matchedOptions) > 0 {
			selectedOptionMatches := false
			for _, label := range matchedOptions {
				if _, isSelected := selected[label]; isSelected {
					selectedOptionMatches = true
					break
				}
			}
			if !selectedOptionMatches {
				return fmt.Sprintf("解析推出的数列仅出现在未选项 %s，与最终答案不一致", strings.Join(matchedOptions, ", "))
			}
		}
	}
	return ""
}

func sequenceKindAt(text string, position int) string {
	start := position - 40
	if start < 0 {
		start = 0
	}
	prefix := strings.ToUpper(text[start:position])
	bfs := strings.LastIndex(prefix, "BFS")
	dfs := strings.LastIndex(prefix, "DFS")
	if bfs > dfs {
		return "BFS"
	}
	if dfs > bfs {
		return "DFS"
	}
	return ""
}

func normalizeSequence(value string) string {
	return strings.NewReplacer(" ", "", "\t", "", "，", ",", "、", ",").Replace(value)
}

func choiceConclusionText(answer string) string {
	conclusionMarkers := []string{"因此", "所以", "故", "故而", "从而", "结论", "可得", "可知", "序列为", "最终", "应选", "正确选项"}
	var conclusions []string
	for _, sentence := range strings.FieldsFunc(answer, func(r rune) bool {
		return strings.ContainsRune("。！？!?；;\n\r", r)
	}) {
		for _, marker := range conclusionMarkers {
			if strings.Contains(sentence, marker) {
				conclusions = append(conclusions, comparableText(sentence))
				break
			}
		}
	}
	return strings.Join(conclusions, " ")
}

func comparableText(value string) string {
	value = strings.TrimSpace(value)
	value = regexp.MustCompile(`(?i)^\s*[A-D]\s*[.．:：、)]\s*`).ReplaceAllString(value, "")
	value = strings.NewReplacer("$", "", "\\", "", "{", "", "}", "", "\n", "", "\r", "", "\t", "", " ", "", "　", "", "，", ",", "。", ".").Replace(value)
	return strings.ToLower(value)
}

func mentionsOptionLabel(answer, label string) bool {
	pattern := regexp.MustCompile(`(?i)(?:(?:选项|option)\s*` + regexp.QuoteMeta(label) + `\b|(?:^|[^a-z0-9])\s*[-*]?\s*\(?` + regexp.QuoteMeta(label) + `\s*(?:选项|项|[.)、．]))`)
	return pattern.MatchString(answer)
}

// ExplanationIssues checks whether an answer contains enough reasoning to be
// useful as a study explanation, rather than only exposing a final option or
// number. The thresholds are intentionally type-aware: English translation
// and writing answers are judged by substantive length, while objective
// questions need an explicit reasoning signal.
func ExplanationIssues(q models.GeneratedQuestion) []string {
	answer := strings.TrimSpace(q.Answer)
	if answer == "" || blankOrPlaceholder(answer) {
		return nil
	}
	text := explanationText(answer)
	if q.Type == models.TypeChoice || q.Type == models.TypeMultiChoice {
		if q.Type == models.TypeMultiChoice {
			if missing := missingOptionExplanations(answer); len(missing) > 0 {
				return []string{fmt.Sprintf("多选题解析未逐项说明 A-D（缺少 %s）", strings.Join(missing, ", "))}
			}
		}
		if isEnglishQuestion(q) && q.Type == models.TypeChoice && !models.IsEnglishPartB(q.Points) {
			if missing := missingOptionExplanations(answer); len(missing) > 0 {
				return []string{fmt.Sprintf("英语选择题解析未覆盖正确选项及干扰项 A-D（缺少 %s）", strings.Join(missing, ", "))}
			}
		}
		if len([]rune(text)) < 28 {
			return []string{"参考答案只有选项结论，缺少解析"}
		}
		if !containsExplanationSignal(text) && !(isEnglishQuestion(q) && containsEnglishExplanationSignal(text)) {
			return []string{"选择题解析缺少依据、推导或排除干扰项的说明"}
		}
		return nil
	}
	if q.Type == models.TypeFillBlank {
		if len([]rune(text)) < 8 {
			return []string{"填空题答案缺少必要计算或公式说明"}
		}
		return nil
	}
	if q.Type == models.TypeMajor {
		minimum := 40
		if isEnglishQuestion(q) {
			minimum = 24
		}
		if len([]rune(text)) < minimum {
			return []string{"解答题解析过短，缺少分步过程"}
		}
		if !containsExplanationSignal(text) && !(isEnglishQuestion(q) && containsEnglishExplanationSignal(text)) {
			return []string{"解答题解析缺少推导过程或结论依据"}
		}
	}
	return nil
}

func missingOptionExplanations(answer string) []string {
	mentioned := make(map[string]struct{}, 4)
	for _, match := range optionMentionPattern.FindAllStringSubmatch(answer, -1) {
		label := ""
		for _, capture := range match[1:] {
			if capture != "" {
				label = capture
				break
			}
		}
		if label != "" {
			mentioned[strings.ToUpper(label)] = struct{}{}
		}
	}
	missing := make([]string, 0, 4)
	for _, label := range []string{"A", "B", "C", "D"} {
		if _, ok := mentioned[label]; !ok {
			missing = append(missing, label)
		}
	}
	return missing
}

func isEnglishQuestion(q models.GeneratedQuestion) bool {
	for _, point := range q.Points {
		normalized := strings.ToLower(strings.TrimSpace(point))
		if strings.Contains(normalized, "英语一") || strings.Contains(normalized, "英语二") ||
			strings.Contains(normalized, "english1") || strings.Contains(normalized, "english2") {
			return true
		}
	}
	return false
}

func explanationText(answer string) string {
	text := strings.NewReplacer("$", "", "\\", "", "{", "", "}", "", "\n", " ", "\t", " ").Replace(answer)
	text = regexp.MustCompile(`(?i)^\s*(?:参考答案|答案|answer)\s*[:：]?\s*[A-H](?:\s*[。.!！,，])?`).ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func containsExplanationSignal(text string) bool {
	lower := strings.ToLower(text)
	for _, signal := range explanationSignals {
		if strings.Contains(lower, strings.ToLower(signal)) {
			return true
		}
	}
	return false
}

func containsEnglishExplanationSignal(text string) bool {
	lower := strings.ToLower(text)
	for _, signal := range englishExplanationSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

// SubjectContentIssues rejects generated math2 content that is outside its
// calculus/linear-algebra syllabus or has no recognisable syllabus point.
func SubjectContentIssues(subject string, points []string, content string) []string {
	if subject == "politics" || subject == "政治" {
		return PoliticsContentIssues(points, content)
	}
	if subject == "408" {
		if len(points) == 0 || !models.Is408Discipline(models.DisciplineFromPoints(points)) {
			return []string{"408 题位缺少有效的四科归属标签"}
		}
		for _, point := range points {
			normalized := strings.ToLower(strings.TrimSpace(point))
			if normalized == "unknown" || normalized == "未知" || normalized == "" || strings.Contains(normalized, "_auto_") {
				return []string{fmt.Sprintf("408 题位知识点 %q 无效，必须先映射到有效考纲主题", point)}
			}
		}
		return DataStructure408Issues(content)
	}
	if subject != "math2" && subject != "数学二" {
		return nil
	}
	for _, point := range points {
		if strings.Contains(point, "级数") || strings.Contains(point, "傅里叶") || strings.Contains(point, "收敛半径") {
			return []string{fmt.Sprintf("知识点 %q 超出数学二考纲（数学二不考无穷级数）", point)}
		}
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
	if strings.Contains(content, "级数") || strings.Contains(content, "傅里叶") || strings.Contains(content, "收敛半径") || infiniteSeriesNotation.MatchString(content) {
		issues = append(issues, "题目内容涉及数学二不考的无穷级数")
	}
	validPoint := false
	for _, point := range points {
		for _, marker := range []string{
			"极限", "连续", "数列", "导数", "微分", "积分", "函数", "方程",
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

// IsPoliticsPointSet reports whether a point path starts in one of the six
// political-theory syllabus modules. It is also used to invalidate stale parse
// caches that only contain unknown or synthetic labels.
func IsPoliticsPointSet(points []string) bool {
	if len(points) < 2 {
		return false
	}
	_, ok := politicsModules[strings.TrimSpace(points[0])]
	return ok
}

// PoliticsContentIssues rejects cross-subject content and non-syllabus labels
// before a generated item can enter a political-theory paper.
func PoliticsContentIssues(points []string, content string) []string {
	var issues []string
	if !IsPoliticsPointSet(points) {
		issues = append(issues, "政治题位没有映射到六个考纲模块中的有效知识点")
	}
	for _, term := range politicsOutOfScopeTokens {
		if strings.Contains(content, term) {
			issues = append(issues, fmt.Sprintf("政治题内容疑似串入其他科目：%s", term))
			break
		}
	}
	if politicsMathNotation.MatchString(content) {
		issues = append(issues, "政治题内容包含数学公式或函数表达式")
	}
	return unique(issues)
}

// NormalizePastQuestion applies corrections for known source-tag errors to
// cached parse results. The source Markdown carries the same correction; this
// migration keeps existing local parse caches useful without reparsing the
// full corpus.
func NormalizePastQuestion(subject string, q models.PastQuestion) models.PastQuestion {
	if (subject == "math2" || subject == "数学二") && q.ID == "math2_2018_21" {
		q.Points = []string{"高等数学", "函数、极限、连续", "数列敛散性的判定"}
	}
	return canonicalizeMathPoints(subject, q)
}

// mathDisciplineByChapter maps a chapter-level math knowledge point to the
// discipline it belongs to. Some historical files label a question with its
// chapter (e.g. 多元函数积分学) instead of the top-level discipline, which makes
// the point path look like a different subject to discipline-aware code and
// leaks a chapter name into prompts and the emitted spec table.
var mathDisciplineByChapter = map[string]string{
	"多元函数微分学":     "高等数学",
	"多元函数积分学":     "高等数学",
	"常微分方程":       "高等数学",
	"微分方程":        "高等数学",
	"无穷级数":        "高等数学",
	"空间解析几何":      "高等数学",
	"向量代数与空间解析几何": "高等数学",
	"函数极限连续":      "高等数学",
	"一元函数微分学":     "高等数学",
	"一元函数积分学":     "高等数学",
	"行列式":         "线性代数",
	"矩阵":          "线性代数",
	"向量":          "线性代数",
	"线性方程组":       "线性代数",
	"特征值与特征向量":    "线性代数",
	"二次型":         "线性代数",
	"随机事件与概率":     "概率论与数理统计",
	"随机变量及其分布":    "概率论与数理统计",
	"随机变量的数字特征":   "概率论与数理统计",
	"大数定律与中心极限定理": "概率论与数理统计",
	"数理统计的基本概念":   "概率论与数理统计",
	"参数估计":        "概率论与数理统计",
	"假设检验":        "概率论与数理统计",
}

// canonicalizeMathPoints prepends the discipline to a math point path that
// starts at a chapter. The prepended label is what math2's syllabus gate and
// models.DisciplineFromPoints expect, so out-of-syllabus questions (e.g. a
// probability item inside Math II) are still rejected after normalization.
func canonicalizeMathPoints(subject string, q models.PastQuestion) models.PastQuestion {
	if !isMathSubject(subject) || len(q.Points) < 2 {
		return q
	}
	discipline, ok := mathDisciplineByChapter[strings.TrimSpace(q.Points[0])]
	if !ok {
		return q
	}
	q.Points = append([]string{discipline}, q.Points...)
	return q
}

func isMathSubject(subject string) bool {
	switch strings.TrimSpace(subject) {
	case "math1", "数学一", "math2", "数学二":
		return true
	default:
		return false
	}
}

// FilterPastQuestions normalizes known source-tag mistakes before rejecting
// past questions whose content or annotations are outside the selected exam.
func FilterPastQuestions(subject string, questions []models.PastQuestion) []models.PastQuestion {
	filtered := make([]models.PastQuestion, 0, len(questions))
	for _, question := range questions {
		question = NormalizePastQuestion(subject, question)
		if PastQuestionInSubject(subject, question) {
			filtered = append(filtered, question)
		}
	}
	return filtered
}

// PastQuestionInSubject filters out-of-syllabus source questions before they
// can influence composition, few-shot examples, or duplicate detection.
func PastQuestionInSubject(subject string, q models.PastQuestion) bool {
	if subject == "politics" || subject == "政治" {
		content := strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n")
		return len(PoliticsContentIssues(q.Points, content)) == 0
	}
	if subject == "408" {
		content := strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n")
		return len(SubjectContentIssues(subject, q.Points, content)) == 0
	}
	if subject != "math2" && subject != "数学二" {
		return true
	}
	q = NormalizePastQuestion(subject, q)
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
	return shortPollutionToken.MatchString(text)
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
