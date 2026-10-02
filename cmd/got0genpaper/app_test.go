package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"GoT0GenPaper/internal/config"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/grader"
)

// sampleExam is a minimal past-exam file in real Markdown format for the parser.
// Uses YAML front matter, ### Section / #### Category / ##### N headers, and
// structured markers (【答案】/【标签】/【解析】) matching the real exam files.
const sampleExam = `---
title: "2024 年 408 真题"
source: test
---

### 选择题

#### 数据结构

##### 1

下列关于顺序存储结构特性的叙述中, 正确的是。

- A. 支持随机访问, 存储密度高
- B. 插入删除方便
- C. 存储密度低
- D. 不可随机访问

**【答案】** A

**【标签】** 顺序存储

**【解析】**
顺序存储支持随机访问，存储密度高。

##### 2

设有一个空栈, 栈顶指针为 1250H, 经过两次进栈操作后栈顶指针的值是多少。

**【答案】** 1258H

**【标签】** 栈

**【解析】**
进栈操作使栈顶指针上移。

### 解答题

#### 数据结构

##### 3

已知有向图的邻接矩阵表示, 请设计一个算法统计出度为零的顶点个数, 并分析时间复杂度。

**【答案】**
遍历邻接矩阵每一行, 行全零即为出度为零的顶点。

**【标签】** 图,邻接矩阵

**【解析】**
时间复杂度为 O(n^2)。
`

// newTestAPI builds an API wired to a temp workspace with a dead LLM endpoint,
// so every LLM call fails fast and the per-stage fallbacks are exercised.
func newTestAPI(t *testing.T) *API {
	t.Helper()
	tmp := t.TempDir()
	a := &API{cfg: config.Default()}
	a.cfg.Paths.DataDir = filepath.Join(tmp, "data")
	a.cfg.Paths.OutputDir = filepath.Join(tmp, "output")
	a.cfg.Paths.Templates = filepath.Join(tmp, "templates")
	a.cfg.ActiveProvider().BaseURL = "http://127.0.0.1:1" // connection refused immediately
	a.rewire()
	a.ctx = context.Background()
	return a
}

func writeExamSource(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "exams")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2024_408.md"), []byte(sampleExam), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// requirePipelineIntegration keeps the default test suite deterministic and
// cheap. Full stage orchestration intentionally remains an opt-in local test:
// even with a dead endpoint it exercises question generation, answer
// production, persistence, and LaTeX assembly rather than a unit contract.
func requirePipelineIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_PIPELINE_TESTS") != "1" {
		t.Skip("set RUN_PIPELINE_TESTS=1 to run full pipeline integration tests")
	}
}

// TestRunPipelineSmoke drives stages 1→7 end-to-end (LLM fallbacks active)
// and verifies the state flow + persistence contract of each stage.
func TestRunPipelineSmoke(t *testing.T) {
	requirePipelineIntegration(t)
	a := newTestAPI(t)
	source := writeExamSource(t)

	summary, err := a.RunPipeline(PipelineRequest{
		Source:          source,
		Blueprint:       smallBlueprint(),
		MaxRepairRounds: 2,
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	if summary.ParseCount < 3 {
		t.Errorf("ParseCount = %d, want >= 3", summary.ParseCount)
	}
	if summary.SpecCount == 0 {
		t.Errorf("SpecCount = 0, want > 0")
	}
	if summary.Generated != summary.SpecCount {
		t.Errorf("Generated = %d, want == SpecCount %d", summary.Generated, summary.SpecCount)
	}
	if summary.Answered != summary.Generated {
		t.Errorf("Answered = %d, want == Generated %d", summary.Answered, summary.Generated)
	}
	if summary.Validation == nil {
		t.Fatal("Validation is nil")
	}
	if summary.Paper == nil {
		t.Fatal("Paper is nil")
	}
	if summary.Paper.TotalScore <= 0 {
		t.Errorf("Paper.TotalScore = %.1f, want > 0", summary.Paper.TotalScore)
	}
	if len(summary.Paper.Rubrics) != summary.Generated {
		t.Errorf("len(Rubrics) = %d, want == Generated %d", len(summary.Paper.Rubrics), summary.Generated)
	}
	if summary.Output == nil || summary.Output.ExamLaTeX == "" || summary.Output.AnswerLaTeX == "" {
		t.Fatal("AssembleOutput incomplete")
	}
	if len(summary.Warnings) != 0 && summary.Validation == nil {
		t.Error("Warnings present but no validation attached")
	}

	// Persistence contract: output dir must contain the deliverables.
	for _, name := range []string{"exam.tex", "answers.tex", "answer_sheet.tex", "spec_table.md", "paper.json"} {
		path := filepath.Join(a.store.OutputDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("output artifact %s missing: %v", name, err)
		}
	}

	// LoadStoredPaper must round-trip the assembled paper.
	loaded, err := a.LoadStoredPaper()
	if err != nil {
		t.Fatalf("LoadStoredPaper: %v", err)
	}
	if len(loaded.Questions) != len(summary.Paper.Questions) {
		t.Errorf("loaded paper has %d questions, want %d", len(loaded.Questions), len(summary.Paper.Questions))
	}
}

func TestRunPipelineZeroRepairRoundsOnlyValidates(t *testing.T) {
	requirePipelineIntegration(t)
	a := newTestAPI(t)
	source := writeExamSource(t)

	summary, err := a.RunPipeline(PipelineRequest{
		Source:          source,
		Blueprint:       smallBlueprint(),
		MaxRepairRounds: 0,
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if summary.RepairRounds != 0 {
		t.Fatalf("RepairRounds = %d, want 0", summary.RepairRounds)
	}
	if summary.Validation == nil {
		t.Fatal("zero-round run must still perform final validation")
	}
}

// TestStageFlowWithState verifies each stage API individually, including
// the "reuse previous stage output when input is empty" behaviour.
func TestStageFlowWithState(t *testing.T) {
	requirePipelineIntegration(t)
	a := newTestAPI(t)
	source := writeExamSource(t)

	// Stage 1.
	res, err := a.ParseExams(source)
	if err != nil {
		t.Fatalf("ParseExams: %v", err)
	}
	if len(res.Questions) < 3 {
		t.Fatalf("parsed %d questions, want >= 3", len(res.Questions))
	}
	for _, q := range res.Questions {
		if q.Stem == "" {
			t.Error("parsed question has empty Stem (few-shot source lost)")
		}
	}

	// Stage 2 with empty input → must fall back to lastParse and wire the orchestrator.
	pred, err := a.PredictWeights(nil)
	if err != nil {
		t.Fatalf("PredictWeights: %v", err)
	}
	if pred.NumPoints == 0 {
		t.Error("PredictWeights returned no points")
	}
	if a.orch.GNNWeights == nil {
		t.Error("GNN weights not wired into orchestrator")
	}

	// Stage 3 with empty candidates → must fall back to lastParse.
	spec, err := a.ComposeExam(smallBlueprint(), nil)
	if err != nil {
		t.Fatalf("ComposeExam: %v", err)
	}
	if len(spec.Entries) == 0 {
		t.Fatal("spec table has no entries")
	}
	if a.lastBlueprint == nil || a.lastSpec == nil {
		t.Error("blueprint/spec not retained in state")
	}

	// Stage 4.
	qs, err := a.GenerateQuestions(*spec)
	if err != nil {
		t.Fatalf("GenerateQuestions: %v", err)
	}
	scoreTotal := 0.0
	for _, q := range qs {
		if q.Score <= 0 {
			t.Errorf("question %s has Score <= 0 (spec score not carried)", q.ID)
		}
		scoreTotal += q.Score
	}

	// Stage 5.
	reviews, err := a.ProduceAnswers(qs)
	if err != nil {
		t.Fatalf("ProduceAnswers: %v", err)
	}
	if len(reviews) != len(qs) {
		t.Fatalf("got %d reviews for %d questions", len(reviews), len(qs))
	}

	// Stage 6 with an explicit blueprint.
	paper := buildPaper(*spec, qs, reviews)
	val, err := a.ValidatePaper(paper, nil) // nil → must use lastBlueprint
	if err != nil {
		t.Fatalf("ValidatePaper: %v", err)
	}
	if val.ScoreActual != scoreTotal {
		t.Errorf("validation ScoreActual = %.1f, want %.1f (score fallback broken)", val.ScoreActual, scoreTotal)
	}

	// Stage 7.
	out, err := a.AssemblePaper(paper)
	if err != nil {
		t.Fatalf("AssemblePaper: %v", err)
	}
	if out.SpecTable == "" {
		t.Error("spec table markdown is empty")
	}

	// GetState must reflect everything retained.
	s := a.GetState()
	if !s.HasParse || !s.HasBlueprint || !s.HasSpec {
		t.Errorf("GetState incomplete: %+v", s)
	}
}

// TestGradeAnswersFallback exercises Stage 8 with the LLM down (fallback path).
func TestGradeAnswersFallback(t *testing.T) {
	a := newTestAPI(t)

	rubric := models.Rubric{
		ID:                "rubric_q1",
		QuestionID:        "q1",
		Items:             []models.RubricItem{{ID: "chk_1", Description: "设变量", MaxScore: 4}, {ID: "chk_2", Description: "求解", MaxScore: 6}},
		AcceptableMethods: []string{"标准解法"},
		Version:           1,
	}
	reqs := []grader.GradeRequest{
		{QuestionID: "q1", StudentAnswer: "设 x=1, 解得 x=2", QuestionStem: "求解方程", MaxScore: 10},
	}
	results, err := a.GradeAnswers(reqs, []models.Rubric{rubric})
	if err != nil {
		t.Fatalf("GradeAnswers: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if len(r.Verdicts) != 2 {
		t.Errorf("got %d verdicts, want 2 (one per rubric item)", len(r.Verdicts))
	}
	if r.TotalScore != 0 || r.Status != "needs_review" || r.AutoScored {
		t.Errorf("fallback result = %+v, want zero score and needs_review", r)
	}

	// Empty answer must score zero.
	reqs[0].StudentAnswer = ""
	results, err = a.GradeAnswers(reqs, []models.Rubric{rubric})
	if err != nil {
		t.Fatalf("GradeAnswers(empty): %v", err)
	}
	if results[0].TotalScore != 0 {
		t.Errorf("empty answer TotalScore = %.1f, want 0", results[0].TotalScore)
	}
}

// TestConfigRoundTrip verifies SaveConfig/UpdateConfig persist and rewire.
func TestConfigRoundTrip(t *testing.T) {
	// UpdateConfig persists to config.toml relative to CWD (the repo root when
	// running `go test ./...`) — chdir into a temp dir so the real config is
	// never overwritten by test data.
	t.Chdir(t.TempDir())
	t.Setenv("GOT0GENPAPER_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	a := newTestAPI(t)

	cfg := a.GetConfig()
	cfg.ActiveProvider().DefaultModel = "test-model-xyz"
	if _, err := a.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if got := a.gen.DefaultModel; got != "test-model-xyz" {
		t.Errorf("generator DefaultModel = %q after rewire, want test-model-xyz", got)
	}
	if got := a.grader.Model; got != cfg.NewAPI.JudgeModel {
		t.Errorf("grader model = %q, want judge model %q", got, cfg.NewAPI.JudgeModel)
	}
}
