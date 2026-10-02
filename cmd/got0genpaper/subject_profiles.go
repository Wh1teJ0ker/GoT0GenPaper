package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
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
	return blueprintForSubjectConfigured(subject, "", "")
}

// blueprintForSubjectConfigured applies a named difficulty preset or an
// explicit easy,medium,hard histogram to a subject blueprint. The histogram
// is kept in the blueprint so composition, generation, validation, and the
// emitted spec table all use the same difficulty contract.
func blueprintForSubjectConfigured(subject, preset, histogram string) (orchestrator.Blueprint, error) {
	return blueprintForSubjectDifficulty(subject, preset, histogram, "")
}

// blueprintForSubjectDifficulty accepts either a ratio histogram or explicit
// integer counts. Counts are useful when a paper has a small fixed number of
// questions and the caller needs an exact easy/medium/hard layout.
func blueprintForSubjectDifficulty(subject, preset, histogram, counts string) (orchestrator.Blueprint, error) {
	p, ok := subjectProfile(subject)
	if !ok {
		return orchestrator.Blueprint{}, fmt.Errorf("不支持的科目 %q，可选科目：408、数学一、数学二、英语一、英语二、政治", subject)
	}
	if (strings.TrimSpace(histogram) != "" || strings.TrimSpace(counts) != "") && !isDefaultDifficultyPreset(preset) {
		return orchestrator.Blueprint{}, fmt.Errorf("--difficulty 不能与自定义难度比例或题数同时指定")
	}
	if strings.TrimSpace(histogram) != "" && strings.TrimSpace(counts) != "" {
		return orchestrator.Blueprint{}, fmt.Errorf("--difficulty-hist 与 --difficulty-counts 不能同时指定")
	}
	var diffHist map[models.DifficultyBand]float64
	var diffCounts map[models.DifficultyBand]int
	strictDifficulty := strings.TrimSpace(histogram) != "" || strings.TrimSpace(counts) != "" || !isDefaultDifficultyPreset(preset)
	var err error
	if strings.TrimSpace(counts) != "" {
		diffCounts, err = parseDifficultyCounts(counts, p.NumQuestions)
		diffHist = difficultyHistogramFromCounts(diffCounts, p.NumQuestions)
	} else if strings.TrimSpace(histogram) != "" {
		diffHist, err = parseDifficultyHistogram(histogram)
		if err == nil {
			diffCounts, err = orchestrator.AllocateDifficultyCounts(diffHist, p.NumQuestions)
		}
	} else {
		diffHist, err = difficultyHistogramForSubject(p.Key, preset)
		if err == nil {
			diffCounts, err = orchestrator.AllocateDifficultyCounts(diffHist, p.NumQuestions)
		}
	}
	if err != nil {
		return orchestrator.Blueprint{}, err
	}
	return orchestrator.Blueprint{
		Subject:               p.Label,
		TotalScore:            p.TotalScore,
		TypeQuota:             cloneTypeQuota(p.TypeQuota),
		TypeCountQuota:        cloneTypeCountQuota(p.TypeCountQuota),
		DisciplineQuota:       cloneDisciplineQuota(p.DisciplineQuota),
		DisciplineCountQuota:  cloneDisciplineCountQuota(p.DisciplineCountQuota),
		DisciplineOrder:       append([]string(nil), p.DisciplineOrder...),
		CoverageFloor:         map[string]float64{},
		BloomQuota:            map[models.CognitiveLevel]int{},
		DiffHist:              diffHist,
		DiffCountQuota:        diffCounts,
		DifficultyCountStrict: strictDifficulty,
		UsageCap:              map[string]int{},
		NumQuestions:          p.NumQuestions,
	}, nil
}

func isDefaultDifficultyPreset(preset string) bool {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "", "default", "subject", "默认", "科目默认":
		return true
	default:
		return false
	}
}

// difficultyHistogramForSubject returns the built-in presets. The subject
// default preserves existing behaviour: math2 is biased toward hard items;
// other subjects use a balanced distribution.
func difficultyHistogramForSubject(subject, preset string) (map[models.DifficultyBand]float64, error) {
	defaults := map[models.DifficultyBand]float64{
		models.BandEasy: 0.3, models.BandMedium: 0.5, models.BandHard: 0.2,
	}
	if subject == "math2" {
		defaults = map[models.DifficultyBand]float64{
			models.BandEasy: 0.1, models.BandMedium: 0.45, models.BandHard: 0.45,
		}
	}
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "", "default", "subject", "默认", "科目默认":
		return defaults, nil
	case "easy", "simple", "low", "偏易", "简单":
		return difficultyHistogram(0.5, 0.4, 0.1), nil
	case "medium", "balanced", "normal", "中等", "均衡":
		return difficultyHistogram(0.2, 0.6, 0.2), nil
	case "hard", "difficult", "high", "偏难", "困难":
		return difficultyHistogram(0.1, 0.45, 0.45), nil
	default:
		return nil, fmt.Errorf("未知难度 %q，可选：default、easy、medium、hard", preset)
	}
}

func difficultyHistogram(easy, medium, hard float64) map[models.DifficultyBand]float64 {
	return map[models.DifficultyBand]float64{
		models.BandEasy: easy, models.BandMedium: medium, models.BandHard: hard,
	}
}

func validateDifficultyHistogram(hist map[models.DifficultyBand]float64) error {
	if hist == nil {
		return fmt.Errorf("难度分布不能为空")
	}
	total := 0.0
	for _, band := range []models.DifficultyBand{models.BandEasy, models.BandMedium, models.BandHard} {
		value := hist[band]
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("难度比例必须是非负有限数")
		}
		total += value
	}
	if math.Abs(total-1) > 1e-6 {
		return fmt.Errorf("难度比例之和必须为 1，当前为 %.6f", total)
	}
	return nil
}

// parseDifficultyHistogram accepts either "0.2,0.6,0.2" or
// "easy=0.2,medium=0.6,hard=0.2". Values are proportions, not percentages.
func parseDifficultyHistogram(raw string) (map[models.DifficultyBand]float64, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 3 {
		return nil, fmt.Errorf("--difficulty-hist 需要 easy,medium,hard 三个比例")
	}
	hist := make(map[models.DifficultyBand]float64, 3)
	named := strings.Contains(parts[0], "=") || strings.Contains(parts[1], "=") || strings.Contains(parts[2], "=")
	seen := make(map[models.DifficultyBand]bool, 3)
	for i, part := range parts {
		part = strings.TrimSpace(part)
		name, valueText := "", part
		if strings.Contains(part, "=") {
			fields := strings.SplitN(part, "=", 2)
			if len(fields) != 2 || strings.TrimSpace(fields[0]) == "" {
				return nil, fmt.Errorf("无效难度比例 %q", part)
			}
			name, valueText = strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		} else if named {
			return nil, fmt.Errorf("难度比例必须全部使用 band=比例 格式")
		}
		value, err := strconv.ParseFloat(valueText, 64)
		if err != nil {
			return nil, fmt.Errorf("无效难度比例 %q", valueText)
		}
		band := models.DifficultyBand(i)
		if named {
			band, err = parseDifficultyBand(name)
			if err != nil {
				return nil, err
			}
		}
		if seen[band] {
			return nil, fmt.Errorf("难度 band 重复: %s", difficultyBandName(band))
		}
		seen[band] = true
		hist[band] = value
	}
	if err := validateDifficultyHistogram(hist); err != nil {
		return nil, err
	}
	return hist, nil
}

// parseDifficultyCounts accepts either "2,10,10" or
// "easy=2,medium=10,hard=10". Counts must cover the whole paper exactly.
func parseDifficultyCounts(raw string, total int) (map[models.DifficultyBand]int, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 3 {
		return nil, fmt.Errorf("--difficulty-counts 需要 easy,medium,hard 三个题数")
	}
	counts := make(map[models.DifficultyBand]int, 3)
	named := strings.Contains(parts[0], "=") || strings.Contains(parts[1], "=") || strings.Contains(parts[2], "=")
	seen := make(map[models.DifficultyBand]bool, 3)
	sum := 0
	for i, part := range parts {
		part = strings.TrimSpace(part)
		name, valueText := "", part
		if strings.Contains(part, "=") {
			fields := strings.SplitN(part, "=", 2)
			if len(fields) != 2 || strings.TrimSpace(fields[0]) == "" {
				return nil, fmt.Errorf("无效难度题数 %q", part)
			}
			name, valueText = strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		} else if named {
			return nil, fmt.Errorf("难度题数必须全部使用 band=题数 格式")
		}
		value, err := strconv.Atoi(valueText)
		if err != nil || value < 0 {
			return nil, fmt.Errorf("无效难度题数 %q", valueText)
		}
		band := models.DifficultyBand(i)
		if named {
			band, err = parseDifficultyBand(name)
			if err != nil {
				return nil, err
			}
		}
		if seen[band] {
			return nil, fmt.Errorf("难度 band 重复: %s", difficultyBandName(band))
		}
		seen[band] = true
		counts[band] = value
		sum += value
	}
	if sum != total {
		return nil, fmt.Errorf("难度题数之和必须为 %d，当前为 %d", total, sum)
	}
	return counts, nil
}

func difficultyHistogramFromCounts(counts map[models.DifficultyBand]int, total int) map[models.DifficultyBand]float64 {
	hist := make(map[models.DifficultyBand]float64, 3)
	for _, band := range []models.DifficultyBand{models.BandEasy, models.BandMedium, models.BandHard} {
		if total > 0 {
			hist[band] = float64(counts[band]) / float64(total)
		}
	}
	return hist
}

func parseDifficultyBand(raw string) (models.DifficultyBand, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "easy", "simple", "low", "偏易", "简单":
		return models.BandEasy, nil
	case "medium", "balanced", "normal", "中等", "均衡":
		return models.BandMedium, nil
	case "hard", "difficult", "high", "偏难", "困难":
		return models.BandHard, nil
	default:
		return 0, fmt.Errorf("未知难度 band %q，可选：easy、medium、hard", raw)
	}
}

func difficultyBandName(band models.DifficultyBand) string {
	switch band {
	case models.BandEasy:
		return "easy"
	case models.BandMedium:
		return "medium"
	case models.BandHard:
		return "hard"
	default:
		return "unknown"
	}
}

func difficultyHistogramView(hist map[models.DifficultyBand]float64) map[string]float64 {
	return map[string]float64{
		"easy": hist[models.BandEasy], "medium": hist[models.BandMedium], "hard": hist[models.BandHard],
	}
}

func difficultyCountView(counts map[models.DifficultyBand]int) map[string]int {
	return map[string]int{
		"easy": counts[models.BandEasy], "medium": counts[models.BandMedium], "hard": counts[models.BandHard],
	}
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
