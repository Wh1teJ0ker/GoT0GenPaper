package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"GoT0GenPaper/internal/config"
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

// newTestApp builds an application wired to a temp workspace with a dead LLM endpoint,
// so every LLM call fails fast and the per-stage fallbacks are exercised.
func newTestApp(t *testing.T) *app {
	t.Helper()
	tmp := t.TempDir()
	a := &app{cfg: config.Default()}
	a.cfg.Paths.DataDir = filepath.Join(tmp, "data")
	a.cfg.Paths.OutputDir = filepath.Join(tmp, "output")
	// Tests still isolate mutable data/output, but use the checked-in template
	// contract so reassembly audits exercise the same LaTeX templates as the
	// CLI instead of logging a spurious missing-template warning.
	a.cfg.Paths.Templates = repoPath("templates")
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

// TestRunPipelineSmoke drives the generation stages end-to-end (LLM fallbacks active)
// and verifies the state flow + persistence contract of each stage.
func TestRunPipelineSmoke(t *testing.T) {
	requirePipelineIntegration(t)
	a := newTestApp(t)
	source := writeExamSource(t)

	summary, err := a.runPipeline(PipelineRequest{
		Source:          source,
		Blueprint:       smallBlueprint(),
		MaxRepairRounds: 2,
	})
	if err != nil {
		t.Fatalf("runPipeline: %v", err)
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
	if summary.Validation == nil {
		t.Fatal("Validation is nil")
	}
	if summary.Paper == nil {
		t.Fatal("Paper is nil")
	}
	if summary.Paper.TotalScore <= 0 {
		t.Errorf("Paper.TotalScore = %.1f, want > 0", summary.Paper.TotalScore)
	}
	if summary.Output == nil || summary.Output.ExamLaTeX == "" {
		t.Fatal("AssembleOutput incomplete")
	}
	if len(summary.Warnings) != 0 && summary.Validation == nil {
		t.Error("Warnings present but no validation attached")
	}

	// Persistence contract: output contains exactly the structured paper and
	// the printable exam source, with no auxiliary booklets or answer sheets.
	entries, err := os.ReadDir(a.store.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "exam.tex" || entries[1].Name() != "paper.json" {
		t.Errorf("output files = %v, want [exam.tex paper.json]", entries)
	}

	// loadStoredPaper must round-trip the assembled paper.
	loaded, err := a.loadStoredPaper()
	if err != nil {
		t.Fatalf("loadStoredPaper: %v", err)
	}
	if len(loaded.Questions) != len(summary.Paper.Questions) {
		t.Errorf("loaded paper has %d questions, want %d", len(loaded.Questions), len(summary.Paper.Questions))
	}
}

func TestRunPipelineZeroRepairRoundsOnlyValidates(t *testing.T) {
	requirePipelineIntegration(t)
	a := newTestApp(t)
	source := writeExamSource(t)

	summary, err := a.runPipeline(PipelineRequest{
		Source:          source,
		Blueprint:       smallBlueprint(),
		MaxRepairRounds: 0,
	})
	if err != nil {
		t.Fatalf("runPipeline: %v", err)
	}
	if summary.RepairRounds != 0 {
		t.Fatalf("RepairRounds = %d, want 0", summary.RepairRounds)
	}
	if summary.Validation == nil {
		t.Fatal("zero-round run must still perform final validation")
	}
}

// TestGenerationStageFlow verifies each stage and cached-output reuse.
func TestGenerationStageFlow(t *testing.T) {
	requirePipelineIntegration(t)
	a := newTestApp(t)
	source := writeExamSource(t)

	// Stage 1.
	res, err := a.parseExams(source)
	if err != nil {
		t.Fatalf("parseExams: %v", err)
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
	pred, err := a.predictWeights(nil)
	if err != nil {
		t.Fatalf("predictWeights: %v", err)
	}
	if pred.NumPoints == 0 {
		t.Error("predictWeights returned no points")
	}
	if a.orch.GNNWeights == nil {
		t.Error("GNN weights not wired into orchestrator")
	}

	// Stage 3 with empty candidates → must fall back to lastParse.
	spec, err := a.composeExam(smallBlueprint(), nil)
	if err != nil {
		t.Fatalf("composeExam: %v", err)
	}
	if len(spec.Entries) == 0 {
		t.Fatal("spec table has no entries")
	}
	if a.lastBlueprint == nil || a.lastSpec == nil {
		t.Error("blueprint/spec not retained in state")
	}

	// Stage 4.
	qs, err := a.generateQuestions(*spec)
	if err != nil {
		t.Fatalf("generateQuestions: %v", err)
	}
	scoreTotal := 0.0
	for _, q := range qs {
		if q.Score <= 0 {
			t.Errorf("question %s has Score <= 0 (spec score not carried)", q.ID)
		}
		scoreTotal += q.Score
	}

	// Stage 5 with an explicit blueprint.
	paper := buildPaper(*spec, qs)
	val, err := a.validatePaper(paper, nil) // nil → must use lastBlueprint
	if err != nil {
		t.Fatalf("validatePaper: %v", err)
	}
	if val.ScoreActual != scoreTotal {
		t.Errorf("validation ScoreActual = %.1f, want %.1f (score fallback broken)", val.ScoreActual, scoreTotal)
	}

	// Stage 6.
	out, err := a.assemblePaper(paper)
	if err != nil {
		t.Fatalf("assemblePaper: %v", err)
	}
	if out.ExamLaTeX == "" {
		t.Error("exam LaTeX is empty")
	}

}
