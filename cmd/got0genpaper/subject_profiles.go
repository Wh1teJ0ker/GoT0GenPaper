package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
)

// SubjectProfile is the product-level contract for a selectable subject.
// SyllabusVerified stays false until an official outline is imported and
// checked against the knowledge-point graph; historical questions alone are
// not evidence that the complete syllabus is covered.
type SubjectProfile struct {
	Key                  string
	Label                string
	Source               string
	TotalScore           float64
	NumQuestions         int
	TypeQuota            map[models.QuestionType]float64
	TypeCountQuota       map[models.QuestionType]int
	DisciplineQuota      map[string]map[models.QuestionType]float64
	DisciplineCountQuota map[string]map[models.QuestionType]int
	DisciplineOrder      []string
	ExamFiles            int
	ParsedQuestions      int
	DataAvailable        bool
	SyllabusStatus       string
	SyllabusVerified     bool
	SyllabusNote         string
}

var selectableSubjects = []SubjectProfile{
	{Key: "408", Label: "408", Source: filepath.Join("exams", "csgraduates", "408"), TotalScore: 150, NumQuestions: 47,
		TypeQuota: map[models.QuestionType]float64{models.TypeChoice: 80, models.TypeMajor: 70}, ExamFiles: 18, ParsedQuestions: 846, DataAvailable: true,
		SyllabusStatus: "结构已核验", SyllabusNote: "已抓取 2026 版 408 公开考纲摘要；完整官方出版物未纳入项目"},
	{Key: "math1", Label: "数学一", Source: filepath.Join("exams", "csgraduates", "math", "math1"), TotalScore: 150, NumQuestions: 22,
		TypeQuota: map[models.QuestionType]float64{models.TypeChoice: 50, models.TypeFillBlank: 30, models.TypeMajor: 70}, TypeCountQuota: map[models.QuestionType]int{models.TypeChoice: 10, models.TypeFillBlank: 6, models.TypeMajor: 6}, ExamFiles: 19, ParsedQuestions: 431, DataAvailable: true,
		SyllabusStatus: "结构已核验", SyllabusNote: "已抓取 2026 版数学一公开章节结构；完整官方出版物未纳入项目"},
	{Key: "math2", Label: "数学二", Source: filepath.Join("exams", "csgraduates", "math", "math2"), TotalScore: 150, NumQuestions: 22,
		TypeQuota: map[models.QuestionType]float64{models.TypeChoice: 50, models.TypeFillBlank: 30, models.TypeMajor: 70}, TypeCountQuota: map[models.QuestionType]int{models.TypeChoice: 10, models.TypeFillBlank: 6, models.TypeMajor: 6},
		DisciplineQuota: map[string]map[models.QuestionType]float64{
			"高等数学": {models.TypeChoice: 35, models.TypeFillBlank: 25, models.TypeMajor: 58},
			"线性代数": {models.TypeChoice: 15, models.TypeFillBlank: 5, models.TypeMajor: 12},
		},
		DisciplineCountQuota: map[string]map[models.QuestionType]int{
			"高等数学": {models.TypeChoice: 7, models.TypeFillBlank: 5, models.TypeMajor: 5},
			"线性代数": {models.TypeChoice: 3, models.TypeFillBlank: 1, models.TypeMajor: 1},
		},
		DisciplineOrder: []string{"高等数学", "线性代数"}, ExamFiles: 19, ParsedQuestions: 431, DataAvailable: true,
		SyllabusStatus: "结构已核验", SyllabusNote: "已抓取 2026 版数学二公开章节结构；完整官方出版物未纳入项目"},
	{Key: "english1", Label: "英语一", Source: filepath.Join("exams", "csgraduates", "english", "english1"), TotalScore: 100, NumQuestions: 52,
		TypeQuota: map[models.QuestionType]float64{models.TypeChoice: 70, models.TypeMajor: 30}, ExamFiles: 17, ParsedQuestions: 880, DataAvailable: true,
		SyllabusStatus: "部分核验", SyllabusNote: "已抓取 2026 版公开结构；词汇附录和官方全文未核验"},
	{Key: "english2", Label: "英语二", Source: filepath.Join("exams", "csgraduates", "english", "english2"), TotalScore: 100, NumQuestions: 48,
		TypeQuota: map[models.QuestionType]float64{models.TypeChoice: 75, models.TypeMajor: 25}, ExamFiles: 17, ParsedQuestions: 816, DataAvailable: true,
		SyllabusStatus: "部分核验", SyllabusNote: "已抓取 2026 版公开结构；词汇附录和官方全文未核验"},
	{Key: "politics", Label: "政治", Source: filepath.Join("exams", "csgraduates", "politics"), TotalScore: 100, NumQuestions: 38,
		TypeQuota: map[models.QuestionType]float64{models.TypeChoice: 16, models.TypeMultiChoice: 34, models.TypeMajor: 50}, ExamFiles: 17, ParsedQuestions: 646, DataAvailable: true,
		SyllabusStatus: "年度过渡", SyllabusNote: "已抓取 2027 公开页面；页面注明正式版本发布后仍会更新"},
}

func subjectProfile(subject string) (SubjectProfile, bool) {
	normalized := strings.TrimSpace(subject)
	for _, p := range selectableSubjects {
		if normalized == p.Key || normalized == p.Label {
			p.TypeQuota = cloneTypeQuota(p.TypeQuota)
			p.TypeCountQuota = cloneTypeCountQuota(p.TypeCountQuota)
			p.DisciplineQuota = cloneDisciplineQuota(p.DisciplineQuota)
			p.DisciplineCountQuota = cloneDisciplineCountQuota(p.DisciplineCountQuota)
			p.DisciplineOrder = append([]string(nil), p.DisciplineOrder...)
			return p, true
		}
	}
	return SubjectProfile{}, false
}

func cloneTypeCountQuota(src map[models.QuestionType]int) map[models.QuestionType]int {
	dst := make(map[models.QuestionType]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneTypeQuota(src map[models.QuestionType]float64) map[models.QuestionType]float64 {
	dst := make(map[models.QuestionType]float64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneDisciplineQuota(src map[string]map[models.QuestionType]float64) map[string]map[models.QuestionType]float64 {
	dst := make(map[string]map[models.QuestionType]float64, len(src))
	for discipline, quota := range src {
		dst[discipline] = cloneTypeQuota(quota)
	}
	return dst
}

func cloneDisciplineCountQuota(src map[string]map[models.QuestionType]int) map[string]map[models.QuestionType]int {
	dst := make(map[string]map[models.QuestionType]int, len(src))
	for discipline, quota := range src {
		dst[discipline] = cloneTypeCountQuota(quota)
	}
	return dst
}

func blueprintForSubject(subject string) (orchestrator.Blueprint, error) {
	p, ok := subjectProfile(subject)
	if !ok {
		return orchestrator.Blueprint{}, fmt.Errorf("不支持的科目 %q，可选科目：408、数学一、数学二、英语一、英语二、政治", subject)
	}
	diffHist := map[models.DifficultyBand]float64{
		models.BandEasy: 0.3, models.BandMedium: 0.5, models.BandHard: 0.2,
	}
	// 数学二本次组卷以中等题为主，减少偏易题，同时保留少量压轴题。
	if p.Key == "math2" {
		diffHist = map[models.DifficultyBand]float64{
			models.BandEasy: 0.2, models.BandMedium: 0.6, models.BandHard: 0.2,
		}
	}
	return orchestrator.Blueprint{
		Subject:              p.Label,
		TotalScore:           p.TotalScore,
		TypeQuota:            cloneTypeQuota(p.TypeQuota),
		TypeCountQuota:       cloneTypeCountQuota(p.TypeCountQuota),
		DisciplineQuota:      cloneDisciplineQuota(p.DisciplineQuota),
		DisciplineCountQuota: cloneDisciplineCountQuota(p.DisciplineCountQuota),
		DisciplineOrder:      append([]string(nil), p.DisciplineOrder...),
		CoverageFloor:        map[string]float64{},
		BloomQuota:           map[models.CognitiveLevel]int{},
		DiffHist:             diffHist,
		UsageCap:             map[string]int{},
		NumQuestions:         p.NumQuestions,
	}, nil
}

func (a *API) sourceForSubject(subject string) (string, error) {
	p, ok := subjectProfile(subject)
	if !ok {
		return "", fmt.Errorf("不支持的科目 %q", subject)
	}
	if a == nil || a.cfg == nil {
		return "", fmt.Errorf("配置未初始化")
	}
	source := filepath.Join(a.cfg.Paths.DataDir, p.Source)
	if _, err := os.Stat(source); err != nil {
		return "", fmt.Errorf("科目 %s 的真题目录不可用: %w", p.Label, err)
	}
	return source, nil
}
