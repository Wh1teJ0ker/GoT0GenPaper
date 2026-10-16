// Package assembler renders generated questions as a printable exam.
package assembler

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"GoT0GenPaper/internal/latex"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/generator"
	"GoT0GenPaper/internal/pipeline/quality"
	"GoT0GenPaper/internal/pipeline/validator"
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
	if issues := validator.EnglishStructureIssues(paper); len(issues) > 0 {
		label := "英语二"
		if models.EnglishSubjectTag(paper.Subject) == "english1" {
			label = "英语一"
		}
		return fmt.Errorf("%s结构校验失败: %s", label, strings.Join(issues, "；"))
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
			wantOptions := quality.ExpectedChoiceOptionCount(q)
			if len(q.Options) != wantOptions {
				return fmt.Errorf("%s 选项数量为 %d，要求 %d，不能编译正式 PDF", label, len(q.Options), wantOptions)
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
	return nil
}

func questionContent(q models.GeneratedQuestion) string {
	parts := []string{q.Stem, q.SharedMaterial, q.Answer}
	parts = append(parts, q.Options...)
	parts = append(parts, q.SharedOptions...)
	return strings.Join(parts, "\n")
}

func isPlaceholder(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "%") || strings.Contains(strings.ToUpper(trimmed), "TODO")
}

// AssembleOutput contains the printable exam source.
type AssembleOutput struct {
	ExamLaTeX string `json:"examLatex"`
}

var ExamFooter = latex.FooterBranding{Right: "GoT0GenPaper"}

// Assemble renders the paper as a single LaTeX exam document.
func (a *Assembler) Assemble(ctx context.Context, paper models.ExamPaper) (*AssembleOutput, error) {
	out := &AssembleOutput{}
	out.ExamLaTeX = a.renderExam(NormalizePaper(paper))
	return out, nil
}

// NormalizePaper repairs known LaTeX and answer-format defects before
// rendering. Papers stored by older versions are fixed here as well, so
// reassembling or recompiling an existing paper yields a clean document without
// regeneration; callers that persist the result also heal the stored JSON.
func NormalizePaper(paper models.ExamPaper) models.ExamPaper {
	paper.Questions = append([]models.GeneratedQuestion(nil), paper.Questions...)
	for i := range paper.Questions {
		q := &paper.Questions[i]
		q.Stem = generator.NormalizeGeneratedLaTeX(q.Stem)
		q.Answer = generator.SyncAnswerSummary(generator.NormalizeGeneratedLaTeX(q.Answer))
		q.SharedMaterial = generator.NormalizeGeneratedLaTeX(q.SharedMaterial)
		for j := range q.Options {
			q.Options[j] = generator.NormalizeGeneratedLaTeX(q.Options[j])
		}
		for j := range q.SharedOptions {
			q.SharedOptions[j] = generator.NormalizeGeneratedLaTeX(q.SharedOptions[j])
		}
	}
	return paper
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
	if models.IsEnglishSubject(paper.Subject) {
		return groupEnglishSections(paper)
	}
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

func groupEnglishSections(paper models.ExamPaper) []sectionGroup {
	groupsBySection := make(map[int]*sectionGroup)
	for i, q := range paper.Questions {
		section, _ := models.EnglishQuestionOrder(q.Points)
		group := groupsBySection[section]
		if group == nil {
			group = &sectionGroup{Title: chineseOrdinal[len(groupsBySection)] + "、" + englishSectionName(section)}
			groupsBySection[section] = group
		}
		group.Questions = append(group.Questions, q)
		group.Numbering = append(group.Numbering, i+1)
	}
	sections := make([]int, 0, len(groupsBySection))
	for section := range groupsBySection {
		sections = append(sections, section)
	}
	sort.Ints(sections)
	groups := make([]sectionGroup, 0, len(sections))
	for ordinal, section := range sections {
		group := groupsBySection[section]
		group.Title = chineseOrdinal[ordinal] + "、" + englishSectionName(section)
		group.Detail = englishSectionDetail(section, group.Questions, group.Numbering)
		groups = append(groups, *group)
	}
	return groups
}

func englishSectionName(section int) string {
	switch section {
	case 0:
		return "完形填空"
	case 1:
		return "阅读理解（Part A）"
	case 2:
		return "新题型（Part B）"
	case 3:
		return "翻译（Part C）"
	case 4:
		return "写作（Part A）"
	case 5:
		return "写作（Part B）"
	default:
		return "英语综合题"
	}
}

func englishSectionDetail(section int, questions []models.GeneratedQuestion, numbering []int) string {
	if len(questions) == 0 {
		return ""
	}
	total := 0.0
	uniform := true
	perQuestion := questions[0].Score
	for _, question := range questions {
		total += question.Score
		if question.Score != perQuestion {
			uniform = false
		}
	}
	detail := fmt.Sprintf("第 %d～%d 小题", numbering[0], numbering[len(numbering)-1])
	if numbering[0] == numbering[len(numbering)-1] {
		detail = fmt.Sprintf("第 %d 小题", numbering[0])
	}
	if uniform && questions[0].Type != models.TypeMajor {
		detail += fmt.Sprintf("，每小题 %s 分，共 %s 分", formatQuestionScore(perQuestion), formatQuestionScore(total))
	} else {
		detail += fmt.Sprintf("，共 %s 分", formatQuestionScore(total))
	}
	switch section {
	case 0:
		detail += "。从各题给出的选项中选出最佳答案"
	case 1:
		detail += "。下列每题给出的四个选项中，只有一个选项最符合试题要求"
	case 2:
		detail += "。按题面要求从备选句中选出最合适的答案"
	}
	return detail
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
		renderedMaterials := make(map[string]bool)
		for i, q := range g.Questions {
			if q.GroupID != "" && !renderedMaterials[q.GroupID] {
				if direction := englishGroupDirection(q); direction != "" {
					sec.Questions = append(sec.Questions, latex.ExamText(direction))
				}
				material := latex.CleanMarkdownStem(q.SharedMaterial)
				if material != "" {
					sec.Questions = append(sec.Questions, latex.ExamText(material))
				}
				if len(q.SharedOptions) > 0 {
					sec.Questions = append(sec.Questions, latex.ExamOptions(q.SharedOptions))
				}
				renderedMaterials[q.GroupID] = true
			}
			stem := latex.CleanMarkdownStem(q.Stem)
			if stem == "" {
				stem = "% TODO: 题面缺失"
			}
			if strings.HasSuffix(q.GroupID, "-cloze") {
				sec.Questions = append(sec.Questions, latex.ClozeQuestion(g.Numbering[i], q.Options))
				continue
			}
			switch q.Type {
			case models.TypeChoice, models.TypeMultiChoice:
				opts := make([]string, 0, len(q.Options))
				if !models.IsEnglishPartB(q.Points) {
					for _, o := range q.Options {
						opts = append(opts, latex.CleanMarkdownStem(o))
					}
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

func englishGroupDirection(question models.GeneratedQuestion) string {
	tag := models.EnglishSubjectTag(subjectOf(question.Points))
	if tag == "" {
		return ""
	}
	position, ok := models.EnglishQuestionPosition(question.Points)
	if !ok {
		return ""
	}
	switch englishGroupKindOf(question.GroupID) {
	case "cloze":
		return "Directions: Read the following text. Choose the best word(s) for each numbered blank and mark A, B, C or D on the ANSWER SHEET. (10 points)"
	case "reading-1":
		if position == 21 {
			return "Directions: Read the following four texts. Answer the questions below each text by choosing A, B, C or D. Mark your answers on the ANSWER SHEET. (40 points)"
		}
	case "part-b":
		if tag == "english1" {
			if position == 41 {
				return "Directions: Read the following text and choose the best sentence from the list A-G for each numbered blank (41-45). There are two extra sentences which you do not need to use. Mark your answers on the ANSWER SHEET. (10 points)"
			}
			return ""
		}
		if position == 41 {
			return "Directions: Choose the best statement from the list A-G for each numbered name (41-45). There are two extra statements which you do not need to use. Mark your answers on the ANSWER SHEET. (10 points)"
		}
	case "translation":
		if position == 46 {
			return "Directions: Read the following text carefully and then translate the underlined segments into Chinese. Write your answers on the ANSWER SHEET. (10 points)"
		}
	}
	return ""
}

// subjectOf recovers the subject tag from a knowledge-point path.
func subjectOf(points []string) string {
	if len(points) == 0 {
		return ""
	}
	return points[0]
}

// englishGroupKindOf classifies a group id by its suffix.
func englishGroupKindOf(groupID string) string {
	switch {
	case strings.HasSuffix(groupID, "-cloze"):
		return "cloze"
	case strings.HasSuffix(groupID, "-reading-1"):
		return "reading-1"
	case strings.HasSuffix(groupID, "-part-b"):
		return "part-b"
	case strings.HasSuffix(groupID, "-translation"):
		return "translation"
	default:
		return "reading"
	}
}

func formatQuestionScore(score float64) string {
	formatted := strconv.FormatFloat(score, 'f', 2, 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	if formatted == "" || formatted == "-0" {
		return "0"
	}
	return formatted
}
