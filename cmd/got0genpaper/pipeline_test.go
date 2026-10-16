package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
	"GoT0GenPaper/internal/pipeline/parser"
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
	}
	return models.ExamPaper{
		Subject:    spec.Subject,
		Questions:  questions,
		SpecTable:  spec,
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
	a := newTestApp(t)
	blueprint := smallBlueprint()
	spec, err := a.orch.Compose(t.Context(), &blueprint, fixtureCandidates())
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	a.lastBlueprint = &blueprint
	paper := fixturePaper(*spec)
	val, err := a.validatePaper(paper, &blueprint)
	if err != nil {
		t.Fatalf("validatePaper: %v", err)
	}
	if !val.ScoreTotalOK || val.ScoreExpected != blueprint.TotalScore {
		t.Errorf("full paper score check = %v/%.1f, want true/%.1f", val.ScoreTotalOK, val.ScoreExpected, blueprint.TotalScore)
	}

	short := paper
	short.Questions = append([]models.GeneratedQuestion(nil), paper.Questions[1:]...)
	short.TotalScore = paper.TotalScore - paper.Questions[0].Score
	val, err = a.validatePaper(short, &blueprint)
	if err != nil {
		t.Fatalf("validatePaper(short): %v", err)
	}
	if val.ScoreTotalOK || val.ScoreExpected != blueprint.TotalScore {
		t.Errorf("short paper score check = %v/%.1f, want false/%.1f", val.ScoreTotalOK, val.ScoreExpected, blueprint.TotalScore)
	}
}

func TestBuildPaperDoesNotInventBlueprintScoreForZeroValueQuestions(t *testing.T) {
	spec := models.SpecTable{Subject: "408", TotalScore: 10}
	paper := buildPaper(spec, []models.GeneratedQuestion{{ID: "q1", SpecID: "spec-1"}})
	if paper.TotalScore != 0 {
		t.Fatalf("paper total = %.1f, want actual zero-value sum", paper.TotalScore)
	}
}

func TestRepairAddsMissingSpecSlot(t *testing.T) {
	a := newTestApp(t)
	blueprint := orchestrator.Blueprint{
		Subject: "408", TotalScore: 10,
		DiffHist:      map[models.DifficultyBand]float64{models.BandMedium: 1},
		CoverageFloor: map[string]float64{"数据结构": 1},
	}
	a.lastBlueprint = &blueprint
	paper := models.ExamPaper{
		Subject:   "408",
		SpecTable: models.SpecTable{Subject: "408", TotalScore: 10, Entries: []models.SpecEntry{{ID: "spec_001", Type: models.TypeMajor, Points: []string{"数据结构", "线性表"}, DiffBand: models.BandMedium, Score: 10}}},
	}
	out, err := a.repairExamPaper(paper, 1)
	if err != nil {
		t.Fatalf("repairExamPaper: %v", err)
	}
	if len(out.Paper.Questions) != 1 || out.Paper.Questions[0].SpecID != "spec_001" {
		t.Fatalf("missing spec slot was not inserted: %+v", out.Paper.Questions)
	}
	if out.Paper.TotalScore != 10 {
		t.Fatalf("repaired total = %.1f, want 10", out.Paper.TotalScore)
	}
}

func TestRepairRegeneratesOnlyViolatedSlot(t *testing.T) {
	a := newTestApp(t)
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
	out, err := a.repairExamPaper(paper, 1)
	if err != nil {
		t.Fatalf("repairExamPaper: %v", err)
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

// TestRepairAppliesKnownRepairWithoutProvider proves the deterministic repair
// path used when fixing an existing paper: a reviewed artifact is fixed from the
// built-in table with no provider request, and a slot whose topic changed is
// left to regeneration instead of receiving the wrong content.
func TestRepairAppliesKnownRepairWithoutProvider(t *testing.T) {
	a := newTestApp(t)
	a.gen.LLM = nil // no provider: the generation path can only produce placeholders

	blueprint := smallBlueprint()
	a.lastBlueprint = &blueprint
	placeholder := func(specID string, points []string, questionType models.QuestionType, score float64) models.GeneratedQuestion {
		return models.GeneratedQuestion{
			ID: "gen_" + specID, SpecID: specID, Type: questionType, Points: points, Score: score,
			Status: "needs_review", Stem: "% TODO: 占位题面", Answer: "% TODO: 占位答案",
			Warnings: []string{"LLM不可用，题面和答案是占位内容"},
		}
	}
	paper := models.ExamPaper{
		Subject: "408", TotalScore: 14,
		SpecTable: models.SpecTable{
			Subject: "408", TotalScore: 14,
			Entries: []models.SpecEntry{
				{ID: "spec_001", Index: 1, Type: models.TypeChoice, Points: []string{"数据结构", "B树", "B+树"}, Score: 2, DiffBand: models.BandMedium},
				{ID: "spec_002", Index: 2, Type: models.TypeChoice, Points: []string{"计算机网络", "TCP", "IP模型"}, Score: 2, DiffBand: models.BandMedium},
				{ID: "spec_003", Index: 3, Type: models.TypeMajor, Points: []string{"操作系统", "进程管理", "进程与线程"}, Score: 10, DiffBand: models.BandMedium},
			},
		},
		Questions: []models.GeneratedQuestion{
			placeholder("spec_001", []string{"数据结构", "B树", "B+树"}, models.TypeChoice, 2),
			placeholder("spec_002", []string{"计算机网络", "TCP", "IP模型"}, models.TypeChoice, 2),
			placeholder("spec_003", []string{"操作系统", "进程管理", "进程与线程"}, models.TypeMajor, 10),
		},
	}

	out, err := a.repairExamPaper(paper, 1)
	if err != nil {
		t.Fatalf("repairExamPaper: %v", err)
	}
	repaired, ok := questionForSpec(&out.Paper, "spec_001")
	if !ok {
		t.Fatal("spec_001 disappeared from the repaired paper")
	}
	if repaired.Status != "ready" || !strings.Contains(repaired.Stem, "关键字数超过 2 个") ||
		!strings.HasPrefix(repaired.Answer, "答案：D") {
		t.Errorf("known repair was not applied: status=%q stem=%q", repaired.Status, repaired.Stem)
	}
	unrelated, _ := questionForSpec(&out.Paper, "spec_002")
	if strings.Contains(unrelated.Stem, "关键字数超过 2 个") || unrelated.Status == "ready" {
		t.Errorf("B-tree repair leaked into the TCP slot: status=%q stem=%q", unrelated.Status, unrelated.Stem)
	}
}

// TestRepairStoredPaperKeepsSlotsWhenProviderIsDown proves a failed repair
// never clobbers the stored paper: the slot that has a deterministic repair is
// fixed, and the slot that would need the provider keeps its previous content
// while the command reports it as unresolved.
func TestRepairStoredPaperKeepsSlotsWhenProviderIsDown(t *testing.T) {
	a := newTestApp(t)
	a.lastParse = &parser.ParseResult{} // skip loading the corpus parse artifact
	a.gen.LLM = nil                     // provider unavailable: only placeholders can be generated

	blueprint := orchestrator.Blueprint{
		Subject: "408", TotalScore: 4,
		DiffHist:      map[models.DifficultyBand]float64{models.BandMedium: 1},
		CoverageFloor: map[string]float64{"数据结构": 1},
	}
	if err := a.store.SaveDataJSON("blueprint", blueprint); err != nil {
		t.Fatal(err)
	}
	placeholder := func(specID string, points []string) models.GeneratedQuestion {
		return models.GeneratedQuestion{
			ID: "gen_" + specID, SpecID: specID, Type: models.TypeChoice, Points: points, Score: 2,
			Status: "needs_review", Stem: "% TODO: 占位题面 " + specID, Answer: "% TODO: 占位答案",
		}
	}
	paper := models.ExamPaper{
		Subject: "408", TotalScore: 4,
		SpecTable: models.SpecTable{
			Subject: "408", TotalScore: 4,
			Entries: []models.SpecEntry{
				{ID: "spec_001", Index: 1, Type: models.TypeChoice, Points: []string{"数据结构", "B树", "B+树"}, Score: 2, DiffBand: models.BandMedium},
				{ID: "spec_002", Index: 2, Type: models.TypeChoice, Points: []string{"数据结构", "二叉树构建"}, Score: 2, DiffBand: models.BandMedium},
			},
		},
		Questions: []models.GeneratedQuestion{
			placeholder("spec_001", []string{"数据结构", "B树", "B+树"}),
			placeholder("spec_002", []string{"数据结构", "二叉树构建"}),
		},
	}
	if err := a.store.SaveOutputJSON("paper", paper); err != nil {
		t.Fatal(err)
	}

	out, err := a.repairStoredPaper([]string{"spec_001", "spec_002"})
	if err == nil || !strings.Contains(err.Error(), "spec_002") {
		t.Fatalf("repairStoredPaper error = %v, want unresolved spec_002", err)
	}
	if out == nil {
		t.Fatal("partial repair returned no result; the fixed slot would be invisible to the user")
	}
	fixed, ok := questionForSpec(&out.Paper, "spec_001")
	if !ok || fixed.Status != "ready" || !strings.Contains(fixed.Stem, "关键字数超过 2 个") {
		t.Errorf("spec_001 was not repaired: ok=%v status=%q stem=%q", ok, fixed.Status, fixed.Stem)
	}
	kept, _ := questionForSpec(&out.Paper, "spec_002")
	if kept.Stem != "% TODO: 占位题面 spec_002" {
		t.Errorf("spec_002 was clobbered by the failed repair: %q", kept.Stem)
	}

	var stored models.ExamPaper
	if err := a.store.LoadJSON(a.store.OutputDir, "paper", &stored); err != nil {
		t.Fatal(err)
	}
	if storedQ, _ := questionForSpec(&stored, "spec_002"); storedQ.Stem != "% TODO: 占位题面 spec_002" {
		t.Errorf("stored spec_002 changed to %q", storedQ.Stem)
	}
	if storedQ, _ := questionForSpec(&stored, "spec_001"); storedQ.Status != "ready" {
		t.Errorf("stored spec_001 was not persisted: status=%q", storedQ.Status)
	}
}

// TestRepairIsIdempotentForReviewedSlots proves running repair twice on a slot
// that already carries the reviewed content succeeds without touching the
// provider: the second run reports the slot as satisfied instead of failing.
func TestRepairIsIdempotentForReviewedSlots(t *testing.T) {
	a := newTestApp(t)
	a.lastParse = &parser.ParseResult{}
	a.gen.LLM = nil // any provider call would fail the test

	blueprint := orchestrator.Blueprint{
		Subject: "408", TotalScore: 2,
		DiffHist:      map[models.DifficultyBand]float64{models.BandMedium: 1},
		CoverageFloor: map[string]float64{"数据结构": 1},
	}
	if err := a.store.SaveDataJSON("blueprint", blueprint); err != nil {
		t.Fatal(err)
	}
	points := []string{"数据结构", "B树", "B+树"}
	placeholder := models.GeneratedQuestion{
		ID: "gen_spec_001", SpecID: "spec_001", Type: models.TypeChoice, Points: points, Score: 2,
		Status: "needs_review", Stem: "% TODO: 占位题面", Answer: "% TODO: 占位答案",
	}
	paper := models.ExamPaper{
		Subject: "408", TotalScore: 2,
		SpecTable: models.SpecTable{Subject: "408", TotalScore: 2, Entries: []models.SpecEntry{
			{ID: "spec_001", Index: 1, Type: models.TypeChoice, Points: points, Score: 2, DiffBand: models.BandMedium},
		}},
		Questions: []models.GeneratedQuestion{placeholder},
	}
	if err := a.store.SaveOutputJSON("paper", paper); err != nil {
		t.Fatal(err)
	}

	if _, err := a.repairStoredPaper([]string{"spec_001"}); err != nil {
		t.Fatalf("first repair: %v", err)
	}
	out, err := a.repairStoredPaper([]string{"spec_001"})
	if err != nil {
		t.Fatalf("second repair must be a no-op success, got: %v", err)
	}
	got, ok := questionForSpec(&out.Paper, "spec_001")
	if !ok || got.Status != "ready" || !strings.Contains(got.Stem, "关键字数超过 2 个") {
		t.Errorf("reviewed content lost after re-repair: ok=%v status=%q stem=%q", ok, got.Status, got.Stem)
	}
}
