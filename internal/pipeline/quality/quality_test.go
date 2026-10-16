package quality

import (
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

func TestExplanationIssuesRejectsBareChoiceAnswer(t *testing.T) {
	q := models.GeneratedQuestion{Type: models.TypeChoice, Answer: "答案：B"}
	issues := ExplanationIssues(q)
	if len(issues) == 0 || !strings.Contains(issues[0], "解析") {
		t.Fatalf("bare choice answer was accepted: %v", issues)
	}
}

func TestExplanationIssuesAcceptsReasonedChoiceAnswer(t *testing.T) {
	q := models.GeneratedQuestion{Type: models.TypeChoice, Answer: "答案：B。根据题设条件代入公式，逐步计算可得该结果，因此选择 B。"}
	if issues := ExplanationIssues(q); len(issues) != 0 {
		t.Fatalf("reasoned choice answer rejected: %v", issues)
	}
}

func Test408RejectsModelAnswerThatRevisesTheQuestionMidExplanation(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:    models.TypeChoice,
		Points:  []string{"408", "操作系统", "地址翻译"},
		Stem:    "根据地址空间和页大小计算虚页号位数，选择正确答案。",
		Options: []string{"A. 20", "B. 32", "C. 12", "D. 10"},
		Answer:  "答案：B。计算得到虚页号 30 位，原选项 B 有误，因此应当修正题面并重新设计选项。",
	}
	issues := Issues(q)
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, " "), "自我纠错") {
		t.Fatalf("self-correcting answer was accepted: %v", issues)
	}
}

func Test408RejectsSpeculationAboutMissingTopology(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeMajor,
		Points: []string{"408", "计算机网络", "路由协议"},
		Stem:   "按给定拓扑计算 RIP 路由。",
		Answer: "答案取决于具体的拓扑结构；假设题目意图为 R1 与 R3 相连。",
	}
	issues := Issues(q)
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, " "), "自我纠错") {
		t.Fatalf("speculation about missing topology was accepted: %v", issues)
	}
}

func TestChoiceConsistencyRejectsDerivedTraversalSequenceForAnotherOption(t *testing.T) {
	q := models.GeneratedQuestion{
		Type: models.TypeChoice,
		Options: []string{
			"A. BFS序列为0, 1, 2, 3, 4；DFS序列为0, 1, 3, 2, 4",
			"B. BFS序列为0, 1, 2, 3, 4；DFS序列为0, 1, 3, 4, 2",
			"C. BFS序列为0, 1, 2, 4, 3；DFS序列为0, 1, 3, 4, 2",
			"D. BFS序列为0, 1, 3, 2, 4；DFS序列为0, 1, 3, 2, 4",
		},
		Answer: "答案：B。BFS序列为0, 1, 2, 3, 4。DFS序列为0, 1, 3, 2, 4，因此选择 B。",
	}
	issues := ChoiceConsistencyIssues(q)
	if len(issues) == 0 || !strings.Contains(issues[0], "未选项 A, D") {
		t.Fatalf("mismatched traversal answer was accepted: %v", issues)
	}
}

func TestMultiChoiceExplanationRequiresEveryOption(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeMultiChoice,
		Answer: "答案：A、C。根据材料，A项和C项成立；B项不符合材料。",
	}
	issues := ExplanationIssues(q)
	if len(issues) == 0 || !strings.Contains(issues[0], "缺少 D") {
		t.Fatalf("incomplete option analysis was accepted: %v", issues)
	}
}

func TestEnglishChoiceExplanationRequiresDistractorAnalysis(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeChoice,
		Points: []string{"英语一", "词汇与语法"},
		Answer: "答案：B（undermined）。根据后文无法返巢可知该词表达削弱，因此选择 B。",
	}
	issues := ExplanationIssues(q)
	if len(issues) == 0 || !strings.Contains(issues[0], "干扰项 A-D") {
		t.Fatalf("English answer without distractor analysis was accepted: %v", issues)
	}
}

func TestEnglish2PartBAllowsAtoGWithoutFourChoiceDistractorRubric(t *testing.T) {
	q := models.GeneratedQuestion{
		Stem:    "Choose the best statement for Robin Chazdon.",
		Type:    models.TypeChoice,
		Points:  []string{"english2", "阅读理解", "Part B", "人物观点匹配"},
		Options: []string{"A", "B", "C", "D", "E", "F", "G"},
		Answer:  "答案：G。根据该段围绕服务调整展开，G 项准确概括中心意思，其他标题无法覆盖文章的核心信息。",
	}
	if issues := Issues(q); len(issues) != 0 {
		t.Fatalf("valid Part B answer rejected: %v", issues)
	}
}

func Test408QualityRejectsVisitedOnlyDirectedCycleDetectionClaim(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeMajor,
		Points: []string{"408", "数据结构", "有向图"},
		Stem:   "分析有向图中的 DFS 环检测。",
		Answer: "能够通过 visited[v] 判断出该回路，因为顶点已经访问过。",
	}
	issues := Issues(q)
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, " "), "递归栈或颜色状态") {
		t.Fatalf("visited-only directed cycle detection was accepted: %v", issues)
	}
}

func Test408QualityAllowsExplicitRecursionStackCycleDetection(t *testing.T) {
	content := "在有向图 DFS 中，visited 标记顶点是否访问；结合递归栈识别回边，才能判断有向环。"
	if issues := DataStructure408Issues(content); len(issues) != 0 {
		t.Fatalf("cycle detection with recursion-stack state was rejected: %v", issues)
	}
}

func Test408RejectsUnknownAndSyntheticKnowledgePoints(t *testing.T) {
	for _, point := range []string{"unknown", "未知", "choice_auto_0"} {
		if issues := SubjectContentIssues("408", []string{point}, "一个完整的计算机基础题面。"); len(issues) == 0 {
			t.Errorf("invalid 408 point %q was accepted", point)
		}
	}
	if issues := SubjectContentIssues("408", []string{"TCP"}, "一个完整的计算机基础题面。"); len(issues) == 0 {
		t.Fatal("408 topic without a four-course discipline was accepted")
	}
}

func Test408PastQuestionFilterDropsUnknownKnowledgePoints(t *testing.T) {
	question := models.PastQuestion{Subject: "408", Type: models.TypeMajor, Points: []string{"unknown"}, Stem: "计算机系统题目", Answer: "合理参考答案"}
	if PastQuestionInSubject("408", question) {
		t.Fatal("past question with unknown 408 label was retained")
	}
}

func TestExplanationIssuesRejectsShortMajorAnswer(t *testing.T) {
	q := models.GeneratedQuestion{Type: models.TypeMajor, Answer: "结果为 1"}
	if issues := ExplanationIssues(q); len(issues) == 0 {
		t.Fatal("short major answer was accepted")
	}
}

func TestExplanationIssuesAcceptsEnglishReferenceText(t *testing.T) {
	q := models.GeneratedQuestion{Type: models.TypeMajor, Answer: "The passage explains the cause and therefore supports the conclusion. The translation should preserve the subject, tense, and logical relationship."}
	if issues := ExplanationIssues(q); len(issues) != 0 {
		t.Fatalf("substantive English answer rejected: %v", issues)
	}
}

func TestChoiceConsistencyRejectsDerivedNonAnswer(t *testing.T) {
	q := models.GeneratedQuestion{
		Type: models.TypeChoice,
		Stem: "图遍历题。",
		Options: []string{
			"A. v1, v2, v3, v4, v5, v6",
			"B. v1, v2, v4, v3, v5, v6",
			"C. v1, v3, v4, v2, v5, v6",
			"D. v1, v3, v4, v5, v6, v2",
		},
		Answer: "答案：D。按 DFS 规则遍历后，整个序列为 v1, v2, v4, v3, v5, v6。",
	}
	issues := ChoiceConsistencyIssues(q)
	if len(issues) == 0 || !strings.Contains(issues[0], "未选项 B") {
		t.Fatalf("inconsistent choice explanation was accepted: %v", issues)
	}
}

func TestChoiceConsistencyIgnoresDistractorValueUsedInIntermediateCalculation(t *testing.T) {
	q := models.GeneratedQuestion{
		Type: models.TypeChoice,
		Stem: "设函数 f 在 x=a 处可导，求 f'(a)。",
		Options: []string{
			"A. $\\frac{f(a)}{a}$",
			"B. $-\\frac{f(a)}{a}$",
			"C. $\\frac{2f(a)}{a}$",
			"D. $-\\frac{2f(a)}{a}$",
		},
		Answer: "答案：A。线段 PQ 的斜率为 $-\\frac{f(a)}{a}$。因此切线斜率取相反数，故 $f'(a)=\\frac{f(a)}{a}$。",
	}
	if issues := ChoiceConsistencyIssues(q); len(issues) != 0 {
		t.Fatalf("intermediate distractor value caused a false positive: %v", issues)
	}
}

func TestChoiceConsistencyRejectsOptionsCopiedIntoStem(t *testing.T) {
	q := models.GeneratedQuestion{
		Type: models.TypeMultiChoice,
		Stem: "下列说法正确的是：A. 主要矛盾居于支配地位。B. 矛盾具有特殊性。C. 普遍性与特殊性相联结。D. 对立彻底消失。",
		Options: []string{
			"A. 主要矛盾居于支配地位。",
			"B. 矛盾具有特殊性。",
			"C. 普遍性与特殊性相联结。",
			"D. 对立彻底消失。",
		},
		Answer: "答案：A、B、C。根据矛盾分析法可得。",
	}
	issues := ChoiceConsistencyIssues(q)
	if len(issues) == 0 || !strings.Contains(issues[0], "重复列出选项") {
		t.Fatalf("duplicated stem options were accepted: %v", issues)
	}
}

func TestChoiceConsistencyAllowsExplicitDistractorLabels(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:    models.TypeChoice,
		Stem:    "Choose the best word for the sentence.",
		Options: []string{"grounded", "justified", "bolstered", "inspired"},
		Answer:  "答案：A（grounded）。分析选项：A) grounded 符合搭配；B) justified 表示正当化；C) bolstered 表示加强；D) inspired 表示启发。",
	}
	if issues := ChoiceConsistencyIssues(q); len(issues) != 0 {
		t.Fatalf("explicitly labeled distractors caused a false positive: %v", issues)
	}
}

func TestEnglishExplanationRecognizesChineseLabelBeforeOptionWord(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeChoice,
		Points: []string{"english2", "完形填空"},
		Stem:   "Choose the best word for the passage.",
		Options: []string{
			"A. fabricate", "B. orchestrate", "C. eliminate", "D. delegate",
		},
		Answer: "答案：B（orchestrate）。根据上下文，B选项表示主动组织交流。A选项fabricate意为捏造，不符合语境；B选项orchestrate意为策划安排，符合语境；C选项eliminate意为消除，与确保交流延续相反；D选项delegate意为委派，不能与此处宾语自然搭配。因此选择B。",
	}
	if issues := Issues(q); len(issues) != 0 {
		t.Fatalf("valid English distractor analysis rejected: %v", issues)
	}
}

func TestEnglishExplanationRecognizesEnglishOptionLabels(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeChoice,
		Points: []string{"english2", "阅读理解", "Part A"},
		Stem:   "Choose the best answer.",
		Options: []string{
			"A. grounded", "B. justified", "C. bolstered", "D. inspired",
		},
		Answer: "答案：A. Option A: grounded fits the context and matches the author's evidence. Option B: justified means moral approval, unlike the intended relation. Choice C: bolstered means strengthened, which changes the meaning. Option D: inspired does not fit the sentence.",
	}
	if issues := Issues(q); len(issues) != 0 {
		t.Fatalf("English option labels were not recognized: %v", issues)
	}
}

func TestEnglishExplanationRecognizesParenthesizedEnglishOptionLabels(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeChoice,
		Points: []string{"英语二", "阅读理解", "Part A"},
		Stem:   "Choose the best answer.",
		Options: []string{
			"A. grounded", "B. justified", "C. bolstered", "D. inspired",
		},
		Answer: "答案：A. (A) fits the context and matches its evidence. (B) changes the meaning to moral approval; (C) means strengthening; (D) means inspired, so these distractors are inappropriate.",
	}
	if issues := Issues(q); len(issues) != 0 {
		t.Fatalf("parenthesized English option labels were not recognized: %v", issues)
	}
}

func TestPollutionCheckDoesNotRejectOrdinaryEnglishVocabulary(t *testing.T) {
	text := "Researchers' attitudes changed as the propagation of ideas spread beneath the canopy; the passage also mentions a mythical story and a Legendre polynomial."
	if Polluted(text) {
		t.Fatalf("ordinary English text was classified as contamination: %q", text)
	}
	if !Polluted("unexpected 0verline artifact") {
		t.Fatal("known malformed marker was not detected")
	}
	if !Polluted("broken conf token") {
		t.Fatal("standalone truncated marker was not detected")
	}
}

func TestEnglishQuestionIssuesRejectsUnpairedClozeBlanksAndMetaText(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeChoice,
		Points: []string{"英语一", "词汇与语法"},
		Stem:   `The passage contains a context. The first word is \underline{\quad}; the second is \underline{\quad}. Choose the second blank.`,
	}
	issues := EnglishQuestionIssues(q)
	if len(issues) != 2 || !strings.Contains(issues[0], "2 个待答空") || !strings.Contains(issues[1], "元叙述") {
		t.Fatalf("malformed English item issues = %v", issues)
	}
}

func TestEnglishQuestionIssuesAllowsOneCompleteClozeBlank(t *testing.T) {
	q := models.GeneratedQuestion{
		Type:   models.TypeChoice,
		Points: []string{"英语一", "完形填空"},
		Stem:   `The projections were \underline{\quad} on evidence rather than speculation. Choose the best word.`,
	}
	if issues := EnglishQuestionIssues(q); len(issues) != 0 {
		t.Fatalf("valid single-blank item was rejected: %v", issues)
	}
}

func TestEnglishQuestionIssuesRejectsUnresolvedMarkersAndMissingContext(t *testing.T) {
	for name, stem := range map[string]string{
		"placeholder":       `Cases began to \textbf{[X]} in several cities. Which word fits [X]?`,
		"question label":    `The executives resigned because of their\n\textit{Question 29}`,
		"missing paragraph": `It can be inferred from Paragraph 3 that the policy failed because`,
	} {
		t.Run(name, func(t *testing.T) {
			q := models.GeneratedQuestion{Type: models.TypeChoice, Points: []string{"英语二", "阅读理解"}, Stem: stem}
			if issues := EnglishQuestionIssues(q); len(issues) == 0 {
				t.Fatalf("malformed English item was accepted: %v", issues)
			}
		})
	}
}

func TestMath2RejectsInfiniteSeriesPointsAndContent(t *testing.T) {
	validPoints := []string{"高等数学", "一元函数积分学", "定积分的计算"}
	for name, points := range map[string][]string{
		"series point": {"高等数学", "无穷级数", "常数项级数敛散性的判定"},
		"power series": {"高等数学", "幂级数的收敛半径"},
	} {
		t.Run(name, func(t *testing.T) {
			if issues := SubjectContentIssues("math2", points, ""); len(issues) == 0 {
				t.Fatalf("out-of-syllabus points were accepted: %v", points)
			}
		})
	}
	for name, content := range map[string]string{
		"series wording":        "讨论无穷级数的敛散性。",
		"infinite sum notation": `计算 $\sum_{n=1}^{\infty} 1/n^2$。`,
		"limits sum notation":   `计算 $\sum\limits_{n=1}^{\infty} 1/n^2$。`,
	} {
		t.Run(name, func(t *testing.T) {
			if issues := SubjectContentIssues("数学二", validPoints, content); len(issues) == 0 {
				t.Fatalf("out-of-syllabus content was accepted: %s", content)
			}
		})
	}
}

func TestMath2RetainsLegacySequenceConvergenceQuestionAfterTagCorrection(t *testing.T) {
	question := models.PastQuestion{
		ID:     "math2_2018_21",
		Source: "math2",
		Points: []string{"高等数学", "无穷级数", "常数项级数敛散性的判定"},
		Stem:   `设数列 $x_n$ 满足 $x_n e^{x_{n+1}}=e^{x_n}-1$，证明数列收敛。`,
	}
	filtered := FilterPastQuestions("math2", []models.PastQuestion{question})
	if len(filtered) != 1 {
		t.Fatalf("correctly tagged sequence question was dropped: %d", len(filtered))
	}
	if got := strings.Join(filtered[0].Points, ","); got != "高等数学,函数、极限、连续,数列敛散性的判定" {
		t.Fatalf("corrected points = %q", got)
	}
	if issues := SubjectContentIssues("math2", filtered[0].Points, filtered[0].Stem); len(issues) != 0 {
		t.Fatalf("in-syllabus sequence convergence was rejected: %v", issues)
	}
}

func TestPoliticsContentRejectsMathAnd408Contamination(t *testing.T) {
	points := []string{"马克思主义基本原理", "唯物辩证法与矛盾分析"}
	for _, content := range []string{
		"设函数 f(x)=e^{2x}，求其导数。",
		"微分方程 y''+4y=sin 2x 的通解为：",
		"矩阵 A 满足 A^2=A，求其秩。",
		"设随机变量 X 服从正态分布，求概率密度。",
		"使用 DFS 遍历该有向图，并分析算法复杂度。",
	} {
		issues := PoliticsContentIssues(points, content)
		if len(issues) == 0 {
			t.Errorf("politics content accepted cross-subject material: %q", content)
		}
	}
}

func TestPoliticsContentAcceptsSyllabusQuestion(t *testing.T) {
	points := []string{"马克思主义基本原理", "唯物辩证法与矛盾分析"}
	content := "马克思主义认为，矛盾双方在一定条件下相互转化。结合材料分析这一原理。"
	if issues := PoliticsContentIssues(points, content); len(issues) != 0 {
		t.Fatalf("valid politics content rejected: %v", issues)
	}
}

func TestPoliticsContentRejectsUnknownOrSyntheticKnowledgePoints(t *testing.T) {
	for _, points := range [][]string{{"unknown"}, {"choice_auto_0"}, {"马克思主义基本原理"}} {
		if issues := PoliticsContentIssues(points, "马克思主义哲学考查矛盾分析法。题目要求说明原理。"); len(issues) == 0 {
			t.Errorf("invalid political syllabus points accepted: %v", points)
		}
	}
}
