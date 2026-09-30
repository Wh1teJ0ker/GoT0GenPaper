package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
)

// realExamDir returns the absolute path to a real past-exam directory.
func realExamDir(t *testing.T, subject string) string {
	t.Helper()
	return repoPath("data", "exams", "csgraduates", subject)
}

// typeSums sums spec-table scores per question type.
func typeSums(entries []models.SpecEntry) map[models.QuestionType]float64 {
	sums := make(map[models.QuestionType]float64)
	for _, e := range entries {
		sums[e.Type] += e.Score
	}
	return sums
}

// assertBlueprintConformance checks the hard (A-layer) guarantees the CP-SAT
// layer promises: blueprint total score and per-type score quotas, hit
// exactly. Question count is a soft target (it follows from the pool's score
// granularity), so it is only asserted where the test opts in.
func assertBlueprintConformance(t *testing.T, bp orchestrator.Blueprint, spec *models.SpecTable) {
	t.Helper()
	if math.Abs(spec.TotalScore-bp.TotalScore) > 0.5 {
		t.Errorf("spec TotalScore = %.1f, want %.1f (compose must land the blueprint total exactly)", spec.TotalScore, bp.TotalScore)
	}
	sums := typeSums(spec.Entries)
	for qt, quota := range bp.TypeQuota {
		if math.Abs(sums[qt]-quota) > 0.5 {
			t.Errorf("type %s score sum = %.1f, want quota %.1f", qt, sums[qt], quota)
		}
	}
	// Intra-paper knowledge-point uniqueness: no two same-type entries may
	// share a point set.
	seen := make(map[string]bool)
	for _, e := range spec.Entries {
		key := fmt.Sprintf("%s|%v", e.Type, e.Points)
		if seen[key] {
			t.Errorf("duplicate spec entry: %s %v appears twice", e.Type, e.Points)
		}
		seen[key] = true
	}
}

// TestComposeBlueprintConformance408 runs CP-SAT against the real 408 exam
// corpus with the default 408 blueprint (40×2 choice + 7×10 major = 150).
func TestComposeBlueprintConformance408(t *testing.T) {
	a := newTestAPI(t)
	if _, err := a.ParseExams(realExamDir(t, "408")); err != nil {
		t.Fatalf("ParseExams: %v", err)
	}
	bp := a.DefaultBlueprint()
	spec, err := a.ComposeExam(bp, nil)
	if err != nil {
		t.Fatalf("ComposeExam: %v", err)
	}
	assertBlueprintConformance(t, bp, spec)
	// 408 quotas are uniform-score (40×2 + 7×10), so the count is exact too.
	if len(spec.Entries) != bp.NumQuestions {
		t.Errorf("spec has %d entries, want NumQuestions %d", len(spec.Entries), bp.NumQuestions)
	}

	// Every spec score must be positive — zero-score slots silently vanish
	// from the assembled paper's total.
	for _, e := range spec.Entries {
		if e.Score <= 0 {
			t.Errorf("spec entry %s (%s) has Score %.1f", e.ID, e.Type, e.Score)
		}
	}
}

// TestComposeBlueprintConformanceMath runs CP-SAT against real math1 exams
// with a math-style blueprint. The candidate pool mixes the pre-2021 format
// (choice/fill 4分) and the 2021+ format (choice/fill 5分), so this also
// probes whether compose can still land the blueprint total.
func TestComposeBlueprintConformanceMath(t *testing.T) {
	a := newTestAPI(t)
	if _, err := a.ParseExams(realExamDir(t, filepath.Join("math", "math1"))); err != nil {
		t.Fatalf("ParseExams: %v", err)
	}
	bp := orchestrator.Blueprint{
		Subject:    "math1",
		TotalScore: 150,
		TypeQuota: map[models.QuestionType]float64{
			models.TypeChoice:    50, // 10×5
			models.TypeFillBlank: 30, // 6×5
			models.TypeMajor:     70, // 6×~12
		},
		TypeCountQuota: map[models.QuestionType]int{
			models.TypeChoice: 10, models.TypeFillBlank: 6, models.TypeMajor: 6,
		},
		BloomQuota: map[models.CognitiveLevel]int{
			models.CogApply:   12,
			models.CogAnalyze: 6,
		},
		DiffHist: map[models.DifficultyBand]float64{
			models.BandEasy:   0.3,
			models.BandMedium: 0.5,
			models.BandHard:   0.2,
		},
		NumQuestions: 22,
	}
	spec, err := a.ComposeExam(bp, nil)
	if err != nil {
		t.Fatalf("ComposeExam: %v", err)
	}
	assertBlueprintConformance(t, bp, spec)
	counts := make(map[models.QuestionType]int)
	for _, entry := range spec.Entries {
		counts[entry.Type]++
	}
	for questionType, want := range bp.TypeCountQuota {
		if counts[questionType] != want {
			t.Errorf("type %s count = %d, want %d", questionType, counts[questionType], want)
		}
	}
}

// TestRunPipelineValidationOnRealCorpus drives the full offline pipeline on
// the real 408 corpus and inspects the whole-paper validation contract.
func TestRunPipelineValidationOnRealCorpus(t *testing.T) {
	a := newTestAPI(t)
	summary, err := a.RunPipeline(PipelineRequest{
		Source:          realExamDir(t, "408"),
		Blueprint:       a.DefaultBlueprint(),
		MaxRepairRounds: 2,
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if summary.Validation == nil {
		t.Fatal("Validation is nil")
	}
	val := summary.Validation

	// The paper must actually total the blueprint score end-to-end.
	if math.Abs(summary.Paper.TotalScore-150) > 0.5 {
		t.Errorf("Paper.TotalScore = %.1f, want 150", summary.Paper.TotalScore)
	}

	// Generated questions inherit knowledge points from the spec table, which
	// is built from past exams — so every question will trip the past-exam
	// dedup check (Jaccard 1.0 against its own source question).
	pastDups := 0
	intraDups := 0
	for _, pair := range val.DuplicatePairs {
		if strings.Contains(pair, "past-exam") {
			pastDups++
		} else if strings.Contains(pair, "intra") {
			intraDups++
		}
	}
	t.Logf("duplicate pairs: %d past-exam, %d intra-paper (KL=%.3f, coverage=%v, score=%v)",
		pastDups, intraDups, val.KL, val.CoverageOK, val.ScoreTotalOK)
	if pastDups > 0 {
		t.Errorf("%d generated questions flagged as past-exam duplicates — the past-exam check must use stem similarity (point sets are inherited from past exams by design), e.g. %q",
			pastDups, val.DuplicatePairs[0])
	}
	if !val.NoDuplicates {
		t.Errorf("NoDuplicates = false: repair can never satisfy this check because regeneration reuses the same knowledge points")
	}

	// Repair contract: validator.ViolatedSpecIDs feed repairPaper, which looks
	// them up in SpecTable entries by ID. Every violated ID must therefore be
	// a spec-entry ID.
	specIDs := make(map[string]bool)
	for _, e := range summary.Paper.SpecTable.Entries {
		specIDs[e.ID] = true
	}
	for _, sid := range val.ViolatedSpecIDs {
		if !specIDs[sid] {
			t.Errorf("ViolatedSpecID %q is not a spec entry ID — repairPaper cannot map it back to a slot", sid)
		}
	}

	// The score check must compare against the blueprint total, not against
	// the paper's own (tautological) sum.
	if val.ScoreExpected != 150 {
		t.Errorf("ScoreExpected = %.1f, want the blueprint total 150 (current check compares paper against itself)", val.ScoreExpected)
	}
}

// TestScoreCheckUsesBlueprintTotal pins the fixed Stage-6 score contract:
// the expected total comes from the blueprint (150), and a paper that falls
// short of it must FAIL the score check instead of comparing against itself.
func TestScoreCheckUsesBlueprintTotal(t *testing.T) {
	a := newTestAPI(t)
	if _, err := a.ParseExams(realExamDir(t, filepath.Join("math", "math1"))); err != nil {
		t.Fatalf("ParseExams: %v", err)
	}
	bp := orchestrator.Blueprint{
		Subject:    "math1",
		TotalScore: 150,
		TypeQuota: map[models.QuestionType]float64{
			models.TypeChoice:    50,
			models.TypeFillBlank: 30,
			models.TypeMajor:     70,
		},
		NumQuestions: 22,
	}
	spec, err := a.ComposeExam(bp, nil)
	if err != nil {
		t.Fatalf("ComposeExam: %v", err)
	}
	if math.Abs(spec.TotalScore-150) > 0.5 {
		t.Fatalf("compose undershoots the blueprint: spec=%.1f, want 150", spec.TotalScore)
	}
	qs, err := a.GenerateQuestions(*spec)
	if err != nil {
		t.Fatalf("GenerateQuestions: %v", err)
	}

	// Full paper passes the score check.
	paper := buildPaper(*spec, qs, nil)
	val, err := a.ValidatePaper(paper, &bp)
	if err != nil {
		t.Fatalf("ValidatePaper: %v", err)
	}
	if !val.ScoreTotalOK || val.ScoreExpected != 150 {
		t.Errorf("full paper: ScoreTotalOK=%v ScoreExpected=%.1f, want true/150", val.ScoreTotalOK, val.ScoreExpected)
	}

	// A short paper must fail the check (ScoreExpected is the blueprint total,
	// not the paper's own sum).
	short := paper
	short.Questions = paper.Questions[1:]
	val, err = a.ValidatePaper(short, &bp)
	if err != nil {
		t.Fatalf("ValidatePaper(short): %v", err)
	}
	if val.ScoreTotalOK {
		t.Errorf("paper is %.0f points short of the blueprint but ScoreTotalOK=true — the check is comparing the paper against itself",
			150-val.ScoreActual)
	}
	if val.ScoreExpected != 150 {
		t.Errorf("ScoreExpected = %.1f, want the blueprint total 150", val.ScoreExpected)
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
	bp := orchestrator.Blueprint{
		Subject:       "408",
		TotalScore:    10,
		DiffHist:      map[models.DifficultyBand]float64{models.BandMedium: 1},
		CoverageFloor: map[string]float64{"point-1": 1},
	}
	a.lastBlueprint = &bp
	paper := models.ExamPaper{
		Subject:    "408",
		SpecTable:  models.SpecTable{Subject: "408", TotalScore: 10, Entries: []models.SpecEntry{{ID: "spec_001", Type: models.TypeMajor, Points: []string{"point-1"}, DiffBand: models.BandMedium, Score: 10}}},
		TotalScore: 0,
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

// TestRepairRegeneratesViolatedSlots exercises the Stage-6 repair protocol
// end-to-end: an out-of-band question is flagged by its SpecID, repairPaper
// regenerates exactly that slot, and the replacement is spec-faithful.
func TestRepairRegeneratesViolatedSlots(t *testing.T) {
	a := newTestAPI(t)
	if _, err := a.ParseExams(realExamDir(t, "408")); err != nil {
		t.Fatalf("ParseExams: %v", err)
	}
	spec, err := a.ComposeExam(a.DefaultBlueprint(), nil)
	if err != nil {
		t.Fatalf("ComposeExam: %v", err)
	}
	qs, err := a.GenerateQuestions(*spec)
	if err != nil {
		t.Fatalf("GenerateQuestions: %v", err)
	}
	paper := buildPaper(*spec, qs, nil)

	// Sabotage one question's difficulty beyond the 0.8 band edge.
	paper.Questions[0].Difficulty = 0.95
	val, err := a.ValidatePaper(paper, nil)
	if err != nil {
		t.Fatalf("ValidatePaper: %v", err)
	}
	if len(val.ViolatedSpecIDs) == 0 {
		t.Fatal("no violations reported for out-of-band difficulty")
	}
	if val.ViolatedSpecIDs[0] != paper.Questions[0].SpecID {
		t.Errorf("violation reported as %q, want the question's SpecID %q", val.ViolatedSpecIDs[0], paper.Questions[0].SpecID)
	}

	out, err := a.RepairPaper(paper, 2)
	if err != nil {
		t.Fatalf("RepairPaper: %v", err)
	}
	if out.Rounds < 1 {
		t.Errorf("repair rounds = %d, want >= 1", out.Rounds)
	}
	for _, q := range out.Paper.Questions {
		if q.Difficulty > 0.8 || q.Difficulty < 0.2 {
			t.Errorf("question %s still out of band after repair: %.2f", q.ID, q.Difficulty)
		}
	}
	if out.Validation == nil {
		t.Fatal("repair output missing final validation")
	}
	for _, q := range out.Paper.Questions {
		if q.SpecID == paper.Questions[0].SpecID && (q.Difficulty > 0.8 || q.Difficulty < 0.2) {
			t.Errorf("spec %s still has out-of-band difficulty after repair: %.2f", q.SpecID, q.Difficulty)
		}
	}
	// Repair must preserve the paper's score integrity.
	if math.Abs(out.Paper.TotalScore-150) > 0.5 {
		t.Errorf("paper total after repair = %.1f, want 150", out.Paper.TotalScore)
	}
}
