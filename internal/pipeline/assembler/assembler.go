// Package assembler implements Stage [7]: 装配层.
//
// Assembles generated questions into the three real-exam deliverables:
//   - exam.tex        试卷: cover + year/subject header + numbered sections
//     (一、单项选择题（第 1~40 小题，每小题 2 分，共 80 分…）) + questions
//     with inline/stacked options, matching the printed 408 collection.
//   - answers.tex     参考答案: per-question answer + rubric.
//   - answer_sheet.tex 答题卡: red-styled machine-readable sheet — 考生信息,
//     准考证号 grid, 注意事项, choice bubble grid, one full-page answer box
//     per non-choice question.
//   - spec_table.md   双向细目表 (post-generation measured attributes).
//
// Compile the .tex files to PDF with latex.CompileTex (xelatex/tectonic).
package assembler

import (
	"context"
	"fmt"
	"math"
	"strings"

	"GoT0GenPaper/internal/latex"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/quality"
)

// Assembler produces the final LaTeX exam paper.
type Assembler struct{}

// New creates an Assembler.
func New() *Assembler { return &Assembler{} }

// ValidateReady checks whether a paper is safe to turn into a final PDF.
// Draft LaTeX can still be assembled for debugging, but placeholders and
// needs_review questions must never be presented as a finished exam.
func ValidateReady(paper models.ExamPaper) error {
	if len(paper.Questions) == 0 {
		return fmt.Errorf("试卷没有题目，不能编译正式 PDF")
	}
	questions := make(map[string]models.GeneratedQuestion, len(paper.Questions))
	for i, q := range paper.Questions {
		label := fmt.Sprintf("第%d题", i+1)
		if strings.TrimSpace(q.ID) == "" {
			return fmt.Errorf("%s 缺少题目 ID，不能编译正式 PDF", label)
		}
		if _, exists := questions[q.ID]; exists {
			return fmt.Errorf("题目 %s ID 重复，不能编译正式 PDF", q.ID)
		}
		questions[q.ID] = q
		if q.Score <= 0 || math.IsNaN(q.Score) || math.IsInf(q.Score, 0) {
			return fmt.Errorf("%s 分值无效，不能编译正式 PDF", label)
		}
		if q.Status == "needs_review" {
			return fmt.Errorf("%s 状态为 needs_review，不能编译正式 PDF", label)
		}
		if strings.TrimSpace(q.Stem) == "" || isPlaceholder(q.Stem) {
			return fmt.Errorf("%s 缺少有效题面，不能编译正式 PDF", label)
		}
		if strings.TrimSpace(q.Answer) == "" || isPlaceholder(q.Answer) {
			return fmt.Errorf("%s 缺少有效参考答案，不能编译正式 PDF", label)
		}
		if q.Type == models.TypeChoice || q.Type == models.TypeMultiChoice {
			if len(q.Options) != 4 {
				return fmt.Errorf("%s 选项数量为 %d，不能编译正式 PDF", label, len(q.Options))
			}
			seenOptions := make(map[string]struct{}, len(q.Options))
			for option, text := range q.Options {
				key := strings.TrimSpace(text)
				if key == "" || isPlaceholder(text) {
					return fmt.Errorf("%s 第%d个选项无效，不能编译正式 PDF", label, option+1)
				}
				if _, exists := seenOptions[key]; exists {
					return fmt.Errorf("%s 包含重复选项，不能编译正式 PDF", label)
				}
				seenOptions[key] = struct{}{}
			}
		}
		if issues := quality.Issues(q); len(issues) > 0 {
			return fmt.Errorf("%s 质量校验失败: %s", label, strings.Join(issues, "；"))
		}
		if issues := quality.SubjectContentIssues(paper.Subject, q.Points, questionContent(q)); len(issues) > 0 {
			return fmt.Errorf("%s 考纲校验失败: %s", label, strings.Join(issues, "；"))
		}
	}
	rubrics := make(map[string]models.Rubric, len(paper.Rubrics))
	for _, rubric := range paper.Rubrics {
		if strings.TrimSpace(rubric.QuestionID) == "" {
			return fmt.Errorf("评分标准缺少题目 ID，不能编译正式 PDF")
		}
		if _, exists := rubrics[rubric.QuestionID]; exists {
			return fmt.Errorf("题目 %s 的评分标准重复，不能编译正式 PDF", rubric.QuestionID)
		}
		if _, exists := questions[rubric.QuestionID]; !exists {
			return fmt.Errorf("评分标准引用未知题目 %s，不能编译正式 PDF", rubric.QuestionID)
		}
		rubrics[rubric.QuestionID] = rubric
		if len(rubric.Items) == 0 {
			return fmt.Errorf("题目 %s 缺少评分检查点，不能编译正式 PDF", rubric.QuestionID)
		}
		total := 0.0
		seenItems := make(map[string]struct{}, len(rubric.Items))
		for _, item := range rubric.Items {
			if strings.TrimSpace(item.ID) == "" || item.MaxScore <= 0 || math.IsNaN(item.MaxScore) || math.IsInf(item.MaxScore, 0) {
				return fmt.Errorf("题目 %s 的评分检查点无效，不能编译正式 PDF", rubric.QuestionID)
			}
			if _, exists := seenItems[item.ID]; exists {
				return fmt.Errorf("题目 %s 的评分检查点重复，不能编译正式 PDF", rubric.QuestionID)
			}
			seenItems[item.ID] = struct{}{}
			total += item.MaxScore
		}
		if math.IsNaN(total) || math.IsInf(total, 0) {
			return fmt.Errorf("题目 %s 的评分标准总分无效，不能编译正式 PDF", rubric.QuestionID)
		}
		if q := questions[rubric.QuestionID]; math.Abs(total-q.Score) > 0.01 {
			return fmt.Errorf("题目 %s 的评分标准总分与题目分值不一致，不能编译正式 PDF", q.ID)
		}
	}
	for _, q := range paper.Questions {
		if _, exists := rubrics[q.ID]; !exists {
			return fmt.Errorf("题目 %s 缺少评分标准，不能编译正式 PDF", q.ID)
		}
	}
	return nil
}

func questionContent(q models.GeneratedQuestion) string {
	return strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n")
}

func isPlaceholder(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "%") || strings.Contains(strings.ToUpper(trimmed), "TODO")
}

// AssembleOutput contains the exam paper + answer sheet + answer key + spec table.
type AssembleOutput struct {
	ExamLaTeX      string `json:"examLatex"`      // 试卷(题序+分值标头)
	AnswerLaTeX    string `json:"answerLatex"`    // 分离答案(rubric+解法族)
	AnswerSheetTeX string `json:"answerSheetTex"` // 答题卡(机读卡样式)
	SpecTable      string `json:"specTable"`      // 双向细目表(生成后实测)
}

// ExamFooter is the three-part page footer of generated papers.
var ExamFooter = latex.FooterBranding{
	Left:   "答案详见参考答案册",
	Center: "",
	Right:  "GoT0GenPaper",
}

// Assemble combines questions, rubrics, and spec into the LaTeX deliverables.
func (a *Assembler) Assemble(ctx context.Context, paper models.ExamPaper) (*AssembleOutput, error) {
	out := &AssembleOutput{}

	specMap := make(map[string]models.SpecEntry)
	for _, e := range paper.SpecTable.Entries {
		specMap[e.ID] = e
	}
	rubricMap := make(map[string]models.Rubric)
	for _, r := range paper.Rubrics {
		rubricMap[r.QuestionID] = r
	}

	out.ExamLaTeX = a.renderExam(paper)
	out.AnswerLaTeX = a.renderAnswers(paper, rubricMap)
	out.AnswerSheetTeX = a.renderAnswerSheet(paper)
	out.SpecTable = a.renderSpecTable(paper, specMap)
	return out, nil
}

// sectionGroup is one contiguous run of same-type questions.
type sectionGroup struct {
	Title     string // 一、单项选择题
	Detail    string // 第 1~40 小题，每小题 2 分，共 80 分。…
	Questions []models.GeneratedQuestion
	Numbering []int // 1-based paper-wide question numbers
}

var chineseOrdinal = []string{"一", "二", "三", "四", "五", "六", "七", "八", "九", "十"}

// groupSections splits paper questions into choice / fill / major sections in
// paper order, building the section intro text from the actual counts and
// scores (uniform per-question scores state 每小题 X 分, variable ones omit it).
func groupSections(paper models.ExamPaper) []sectionGroup {
	order := []models.QuestionType{models.TypeChoice, models.TypeMultiChoice, models.TypeFillBlank, models.TypeMajor}
	byType := make(map[models.QuestionType][]int) // type → question indices
	for i, q := range paper.Questions {
		byType[q.Type] = append(byType[q.Type], i)
	}

	var groups []sectionGroup
	ordinal := 0
	for _, t := range order {
		idxs := byType[t]
		if len(idxs) == 0 {
			continue
		}
		g := sectionGroup{Title: chineseOrdinal[ordinal] + "、" + sectionName(t)}
		ordinal++

		first, last := idxs[0], idxs[len(idxs)-1]
		total := 0.0
		uniform := true
		scorePerQ := paper.Questions[first].Score
		for _, i := range idxs {
			q := paper.Questions[i]
			total += q.Score
			if q.Score != scorePerQ {
				uniform = false
			}
		}
		detail := fmt.Sprintf("第 %d～%d 小题", first+1, last+1)
		if uniform && scorePerQ > 0 {
			detail += fmt.Sprintf("，每小题 %.0f 分，共 %.0f 分", scorePerQ, total)
		} else {
			detail += fmt.Sprintf("，共 %.0f 分", total)
		}
		if t == models.TypeChoice {
			detail += "。下列每题给出的四个选项中，只有一个选项最符合试题要求"
		} else if t == models.TypeMultiChoice {
			detail += "。下列每题给出的四个选项中，有两个或两个以上选项符合试题要求"
		}
		g.Detail = detail

		for _, i := range idxs {
			g.Questions = append(g.Questions, paper.Questions[i])
			g.Numbering = append(g.Numbering, i+1)
		}
		groups = append(groups, g)
	}
	return groups
}

func sectionName(t models.QuestionType) string {
	switch t {
	case models.TypeChoice:
		return "单项选择题"
	case models.TypeMultiChoice:
		return "多项选择题"
	case models.TypeFillBlank:
		return "填空题"
	default:
		return "综合应用题"
	}
}

func examMeta(paper models.ExamPaper) latex.ExamMeta {
	meta := latex.ExamMeta{Subject: paper.Subject}
	if paper.Subject == "408" {
		meta.FullTitle = "计算机科学与技术学科联考计算机学科专业基础综合试题"
	} else if paper.Subject != "" {
		meta.FullTitle = paper.Subject + "试题"
	}
	return meta
}

// renderExam produces the exam paper LaTeX in the printed-collection style.
func (a *Assembler) renderExam(paper models.ExamPaper) string {
	groups := groupSections(paper)

	var sections []latex.ExamSection
	for _, g := range groups {
		sec := latex.ExamSection{Spec: latex.SectionSpec{Title: g.Title, Detail: g.Detail}}
		for i, q := range g.Questions {
			stem := latex.CleanMarkdownStem(q.Stem)
			if stem == "" {
				stem = "% TODO: 题面缺失"
			}
			switch q.Type {
			case models.TypeChoice, models.TypeMultiChoice:
				opts := make([]string, 0, len(q.Options))
				for _, o := range q.Options {
					opts = append(opts, latex.CleanMarkdownStem(o))
				}
				sec.Questions = append(sec.Questions, latex.ChoiceQuestion(g.Numbering[i], stem, opts))
			default:
				sec.Questions = append(sec.Questions, latex.MajorQuestion(g.Numbering[i], q.Score, stem))
			}
		}
		sections = append(sections, sec)
	}

	cover := []string{"全国硕士研究生入学统一考试"}
	if m := examMeta(paper); m.FullTitle != "" {
		cover = append(cover, m.FullTitle)
	}
	cover = append(cover, "模拟试题")

	return latex.ExamPaper(examMeta(paper), sections, &cover, ExamFooter)
}

// renderAnswers produces the answer key with rubrics.
func (a *Assembler) renderAnswers(paper models.ExamPaper, rubricMap map[string]models.Rubric) string {
	var entries []latex.AnswerEntry
	for i, q := range paper.Questions {
		answer := latex.CleanMarkdownStem(q.Answer)
		if answer == "" {
			answer = "% TODO: 参考答案缺失"
		}
		var rubricItems []string
		if r, ok := rubricMap[q.ID]; ok {
			for _, item := range r.Items {
				rubricItems = append(rubricItems,
					fmt.Sprintf("%s (%.0f分): %s", item.ID, item.MaxScore, item.Description))
			}
			if len(r.AcceptableMethods) > 0 {
				rubricItems = append(rubricItems, "可接受解法: "+strings.Join(r.AcceptableMethods, ", "))
			}
		}
		entries = append(entries, latex.AnswerEntry{Number: i + 1, Answer: answer, RubricItems: rubricItems})
	}
	return latex.AnswerKey(examMeta(paper), entries, ExamFooter)
}

// renderAnswerSheet produces the machine-readable 答题卡 LaTeX.
func (a *Assembler) renderAnswerSheet(paper models.ExamPaper) string {
	spec := latex.AnswerSheetSpec{
		Title:               "全国硕士研究生招生考试",
		CardName:            paper.Subject + "答题卡1",
		BarcodeOnFirstMajor: true,
		ChoicePageSize:      40,
	}

	// Score intro strings mirroring the real sheet (colon style).
	var choiceNums, fillNums, majorNums []int
	choiceTotal, choicePerQ := 0.0, 0.0
	fillTotal, fillPerQ := 0.0, 0.0
	majorTotal := 0.0
	for i, q := range paper.Questions {
		switch q.Type {
		case models.TypeChoice, models.TypeMultiChoice:
			choiceNums = append(choiceNums, i+1)
			choiceTotal += q.Score
			choicePerQ = q.Score
		case models.TypeFillBlank:
			fillNums = append(fillNums, i+1)
			fillTotal += q.Score
			fillPerQ = q.Score
		default:
			majorNums = append(majorNums, i+1)
			majorTotal += q.Score
		}
	}

	if len(choiceNums) > 0 {
		spec.ChoiceNums = choiceNums
		spec.ChoiceSection = fmt.Sprintf("一、单项选择题:%d～%d小题,每小题%.0f分,共%.0f分。",
			choiceNums[0], choiceNums[len(choiceNums)-1], choicePerQ, choiceTotal)
	}
	if len(fillNums) > 0 {
		spec.FillNums = fillNums
		spec.FillSection = fmt.Sprintf("二、填空题:%d～%d小题,每小题%.0f分,共%.0f分。",
			fillNums[0], fillNums[len(fillNums)-1], fillPerQ, fillTotal)
	}
	if len(majorNums) > 0 {
		spec.MajorNums = majorNums
		spec.MajorSection = fmt.Sprintf("三、综合应用题:%d～%d小题,共%.0f分。",
			majorNums[0], majorNums[len(majorNums)-1], majorTotal)
	}

	return latex.AnswerSheet(spec)
}

// renderSpecTable produces a text-based 双向细目表 with measured attributes.
func (a *Assembler) renderSpecTable(paper models.ExamPaper, specMap map[string]models.SpecEntry) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# 双向细目表 — %s (总分: %.0f)\n\n", paper.Subject, paper.TotalScore))
	targetCounts := make(map[models.DifficultyBand]int)
	if len(paper.SpecTable.DifficultyTarget) > 0 {
		for band, count := range paper.SpecTable.DifficultyTarget {
			targetCounts[band] = count
		}
	}
	actualCounts := make(map[models.DifficultyBand]int)
	for _, q := range paper.Questions {
		if len(paper.SpecTable.DifficultyTarget) == 0 {
			if spec, ok := specMap[q.SpecID]; ok {
				targetCounts[spec.DiffBand]++
			}
		}
		actualCounts[bandFromDifficulty(q.Difficulty)]++
	}
	b.WriteString(fmt.Sprintf("难度题数目标：易 %d / 中 %d / 难 %d；实际：易 %d / 中 %d / 难 %d。\n\n",
		targetCounts[models.BandEasy], targetCounts[models.BandMedium], targetCounts[models.BandHard],
		actualCounts[models.BandEasy], actualCounts[models.BandMedium], actualCounts[models.BandHard]))
	b.WriteString("| 序号 | 题型 | 知识点 | 认知层 | 难度band | 目标难度 | 实测难度 | 分值 |\n")
	b.WriteString("|------|------|--------|--------|----------|----------|----------|------|\n")

	for i, q := range paper.Questions {
		spec := specMap[q.SpecID]
		b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %d | %.2f | %.2f | %.0f |\n",
			i+1,
			q.Type,
			strings.Join(q.Points, ", "),
			spec.Cognitive,
			spec.DiffBand,
			bandToFloat(spec.DiffBand),
			q.Difficulty,
			a.lookupScore(q, specMap),
		))
	}

	return b.String()
}

func bandFromDifficulty(difficulty float64) models.DifficultyBand {
	return models.BandFromDifficulty(difficulty)
}

// --- Helpers ---

func (a *Assembler) lookupScore(q models.GeneratedQuestion, specMap map[string]models.SpecEntry) float64 {
	if q.Score > 0 {
		return q.Score
	}
	if spec, ok := specMap[q.SpecID]; ok {
		return spec.Score
	}
	return 10 // fallback
}

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
