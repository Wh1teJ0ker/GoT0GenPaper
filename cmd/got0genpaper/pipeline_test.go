package main

import (
	"fmt"
	"math"
	"testing"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
)

// fixtureCandidates is deliberately small. Default tests exercise the
// composition contract with deterministic in-memory data; full historical
// corpus runs belong to an explicit integration command.
func fixtureCandidates() []models.PastQuestion {
	return []models.PastQuestion{
		{ID: "c1", Subject: "408", Type: models.TypeChoice, Points: []string{"数据结构", "线性表"}, Cognitive: models.CogApply, Difficulty: 0.25, Score: 2, Stem: "有效选择题一"},
		{ID: "c2", Subject: "408", Type: models.TypeChoice, Points: []string{"数据结构", "栈"}, Cognitive: models.CogApply, Difficulty: 0.50, Score: 2, Stem: "有效选择题二"},
		{ID: "c3", Subject: "408", Type: models.TypeChoice, Points: []string{"数据结构", "队列"}, Cognitive: models.CogAnalyze, Difficulty: 0.75, Score: 2, Stem: "有效选择题三"},
		{ID: "c4", Subject: "408", Type: models.TypeChoice, Points: []string{"数据结构", "树"}, Cognitive: models.CogAnalyze, Difficulty: 0.75, Score: 2, Stem: "有效选择题四"},
		{ID: "m1", Subject: "408", Type: models.TypeMajor, Points: []string{"数据结构", "图"}, Cognitive: models.CogAnalyze, Difficulty: 0.50, Score: 10, Stem: "有效解答题一"},
		{ID: "m2", Subject: "408", Type: models.TypeMajor, Points: []string{"数据结构", "排序"}, Cognitive: models.CogAnalyze, Difficulty: 0.75, Score: 10, Stem: "有效解答题二"},
	}
}

func smallBlueprint() orchestrator.Blueprint {
	return orchestrator.Blueprint{
		Subject:    "408",
		TotalScore: 14,
		TypeQuota: map[models.QuestionType]float64{
			models.TypeChoice: 4,
			models.TypeMajor:  10,
		},
		TypeCountQuota: map[models.QuestionType]int{
			models.TypeChoice: 2,
			models.TypeMajor:  1,
		},
		DiffHist: map[models.DifficultyBand]float64{
			models.BandEasy:   0,
			models.BandMedium: 1,
			models.BandHard:   0,
		},
		NumQuestions: 3,
	}
}

func fixturePaper(spec models.SpecTable) models.ExamPaper {
	questions := make([]models.GeneratedQuestion, 0, len(spec.Entries))
	rubrics := make([]models.Rubric, 0, len(spec.Entries))
	for _, entry := range spec.Entries {
		q := models.GeneratedQuestion{
			ID:         "gen_" + entry.ID,
			SpecID:     entry.ID,
			Stem:       "这是用于校验的有效题面。",
			Answer:     "这是用于校验的有效答案。",
			Type:       entry.Type,
			Points:     entry.Points,
			Score:      entry.Score,
			Difficulty: difficultyCenter(entry.DiffBand),
			Status:     "ready",
		}
		questions = append(questions, q)
		rubrics = append(rubrics, models.Rubric{
			ID:         "rubric_" + q.ID,
			QuestionID: q.ID,
			Items:      []models.RubricItem{{ID: "check_1", Description: "答案完整", MaxScore: q.Score}},
			Version:    1,
		})
	}
	return models.ExamPaper{
		Subject:    spec.Subject,
		Questions:  questions,
		SpecTable:  spec,
		Rubrics:    rubrics,
		TotalScore: spec.TotalScore,
	}
}

func difficultyCenter(band models.DifficultyBand) float64 {
	switch band {
	case models.BandEasy:
		return 0.25
	case models.BandHard:
		return 0.75
	default:
		return 0.50
	}
}

func typeSums(entries []models.SpecEntry) map[models.QuestionType]float64 {
	sums := make(map[models.QuestionType]float64)
	for _, entry := range entries {
		sums[entry.Type] += entry.Score
	}
	return sums
}

func assertBlueprintConformance(t *testing.T, bp orchestrator.Blueprint, spec *models.SpecTable) {
	t.Helper()
	if math.Abs(spec.TotalScore-bp.TotalScore) > 0.5 {
		t.Errorf("spec total = %.1f, want %.1f", spec.TotalScore, bp.TotalScore)
	}
	sums := typeSums(spec.Entries)
	for questionType, quota := range bp.TypeQuota {
		if math.Abs(sums[questionType]-quota) > 0.5 {
			t.Errorf("type %s score = %.1f, want %.1f", questionType, sums[questionType], quota)
		}
	}
	seen := make(map[string]bool)
	for _, entry := range spec.Entries {
		key := fmt.Sprintf("%s|%v", entry.Type, entry.Points)
		if seen[key] {
			t.Errorf("duplicate spec entry: %s", key)
		}
		seen[key] = true
	}
}

func TestComposeBlueprintConformance408(t *testing.T) {
	blueprint := smallBlueprint()
	spec, err := orchestrator.New(nil, nil).Compose(t.Context(), &blueprint, fixtureCandidates())
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	assertBlueprintConformance(t, blueprint, spec)
	if len(spec.Entries) != blueprint.NumQuestions {
		t.Fatalf("spec has %d entries, want %d", len(spec.Entries), blueprint.NumQuestions)
	}
}

func TestComposeBlueprintConformanceMath(t *testing.T) {
	blueprint := orchestrator.Blueprint{
		Subject: "math1", TotalScore: 30, NumQuestions: 6,
		TypeQuota: map[models.QuestionType]float64{
			models.TypeChoice: 10, models.TypeFillBlank: 10, models.TypeMajor: 10,
		},
		TypeCountQuota: map[models.QuestionType]int{
			models.TypeChoice: 2, models.TypeFillBlank: 2, models.TypeMajor: 2,
		},
	}
	candidates := []models.PastQuestion{
		{ID: "choice-1", Type: models.TypeChoice, Points: []string{"高等数学", "极限"}, Difficulty: 0.3, Score: 5},
		{ID: "choice-2", Type: models.TypeChoice, Points: []string{"高等数学", "导数"}, Difficulty: 0.5, Score: 5},
		{ID: "fill-1", Type: models.TypeFillBlank, Points: []string{"高等数学", "积分"}, Difficulty: 0.3, Score: 5},
		{ID: "fill-2", Type: models.TypeFillBlank, Points: []string{"高等数学", "微分方程"}, Difficulty: 0.5, Score: 5},
		{ID: "major-1", Type: models.TypeMajor, Points: []string{"高等数学", "多元函数"}, Difficulty: 0.5, Score: 5},
		{ID: "major-2", Type: models.TypeMajor, Points: []string{"高等数学", "重积分"}, Difficulty: 0.7, Score: 5},
	}
	spec, err := orchestrator.New(nil, nil).Compose(t.Context(), &blueprint, candidates)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	assertBlueprintConformance(t, blueprint, spec)
	counts := make(map[models.QuestionType]int)
	for _, entry := range spec.Entries {
		counts[entry.Type]++
	}
	for questionType, want := range blueprint.TypeCountQuota {
		if counts[questionType] != want {
			t.Errorf("type %s count = %d, want %d", questionType, counts[questionType], want)
		}
	}
}

func TestScoreCheckUsesBlueprintTotal(t *testing.T) {
	a := newTestAPI(t)
	blueprint := smallBlueprint()
	spec, err := a.orch.Compose(t.Context(), &blueprint, fixtureCandidates())
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	a.lastBlueprint = &blueprint
	paper := fixturePaper(*spec)
	val, err := a.ValidatePaper(paper, &blueprint)
	if err != nil {
		t.Fatalf("ValidatePaper: %v", err)
	}
	if !val.ScoreTotalOK || val.ScoreExpected != blueprint.TotalScore {
		t.Errorf("full paper score check = %v/%.1f, want true/%.1f", val.ScoreTotalOK, val.ScoreExpected, blueprint.TotalScore)
	}

	short := paper
	short.Questions = append([]models.GeneratedQuestion(nil), paper.Questions[1:]...)
	short.TotalScore = paper.TotalScore - paper.Questions[0].Score
	val, err = a.ValidatePaper(short, &blueprint)
	if err != nil {
		t.Fatalf("ValidatePaper(short): %v", err)
	}
	if val.ScoreTotalOK || val.ScoreExpected != blueprint.TotalScore {
		t.Errorf("short paper score check = %v/%.1f, want false/%.1f", val.ScoreTotalOK, val.ScoreExpected, blueprint.TotalScore)
	}
}

func TestBuildPaperDoesNotInventBlueprintScoreForZeroValueQuestions(t *testing.T) {
	spec := models.SpecTable{Subject: "408", TotalScore: 10}
	paper := buildPaper(spec, []models.GeneratedQuestion{{ID: "q1", SpecID: "spec-1"}}, nil)
	if paper.TotalScore != 0 {
		t.Fatalf("paper total = %.1f, want actual zero-value sum", paper.TotalScore)
	}
}

func TestRepairAddsMissingSpecSlot(t *testing.T) {
	a := newTestAPI(t)
	blueprint := orchestrator.Blueprint{
		Subject: "408", TotalScore: 10,
		DiffHist:      map[models.DifficultyBand]float64{models.BandMedium: 1},
		CoverageFloor: map[string]float64{"point-1": 1},
	}
	a.lastBlueprint = &blueprint
	paper := models.ExamPaper{
		Subject:   "408",
		SpecTable: models.SpecTable{Subject: "408", TotalScore: 10, Entries: []models.SpecEntry{{ID: "spec_001", Type: models.TypeMajor, Points: []string{"point-1"}, DiffBand: models.BandMedium, Score: 10}}},
	}
	out, err := a.RepairPaper(paper, 1)
	if err != nil {
		t.Fatalf("RepairPaper: %v", err)
	}
	if len(out.Paper.Questions) != 1 || out.Paper.Questions[0].SpecID != "spec_001" {
		t.Fatalf("missing spec slot was not inserted: %+v", out.Paper.Questions)
	}
	if out.Paper.TotalScore != 10 {
		t.Fatalf("repaired total = %.1f, want 10", out.Paper.TotalScore)
	}
}

func TestRepairRegeneratesOnlyViolatedSlot(t *testing.T) {
	a := newTestAPI(t)
	a.gen.LLM = nil // deterministic placeholder; this test does not call a provider
	blueprint := smallBlueprint()
	spec, err := a.orch.Compose(t.Context(), &blueprint, fixtureCandidates())
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	a.lastBlueprint = &blueprint
	paper := fixturePaper(*spec)
	original := append([]models.GeneratedQuestion(nil), paper.Questions[1:]...)
	paper.Questions[0].Difficulty = 0.95
	out, err := a.RepairPaper(paper, 1)
	if err != nil {
		t.Fatalf("RepairPaper: %v", err)
	}
	if out.Rounds != 1 || out.Validation == nil {
		t.Fatalf("repair result = rounds %d validation %v, want one bounded round", out.Rounds, out.Validation)
	}
	if out.Paper.Questions[0].Difficulty > 0.8 || out.Paper.Questions[0].Difficulty < 0.2 {
		t.Errorf("repaired question remains out of band: %.2f", out.Paper.Questions[0].Difficulty)
	}
	for i, question := range original {
		if question.ID != out.Paper.Questions[i+1].ID {
			t.Errorf("untouched question %d changed from %s to %s", i, question.ID, out.Paper.Questions[i+1].ID)
		}
	}
}
