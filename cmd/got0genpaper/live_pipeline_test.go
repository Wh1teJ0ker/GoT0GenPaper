package main

// Live end-to-end test against a real LLM gateway. Skipped unless LIVE_LLM=1:
//
//	LIVE_LLM=1 go test ./cmd/got0genpaper -run TestLiveFullPipeline -v
//
// Requires config.toml pointed at a reachable gateway (base_url + api_key).
// Drives the same stage calls as RunPipeline with per-stage attribution:
// parse(annotate) → gnn → compose → generate×N → answer×N → validate/repair
// → assemble → compile PDF, then probes the output quality.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"GoT0GenPaper/internal/config"
	"GoT0GenPaper/internal/latex"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
)

func TestLiveFullPipeline(t *testing.T) {
	if os.Getenv("LIVE_LLM") != "1" {
		t.Skip("set LIVE_LLM=1 (and a reachable config.toml) to run the live pipeline")
	}
	source := os.Getenv("LIVE_SOURCE")
	if source == "" {
		source = repoPath("data", "exams", "csgraduates", "408", "2024.md")
	}

	a := newTestAPI(t) // temp workspace; newTestAPI forces a dead LLM URL
	// Re-apply the REAL gateway config (keep the temp workspace paths).
	liveCfg, err := config.Load(config.ConfigPath())
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	paths := a.cfg.Paths
	a.cfg = liveCfg
	a.cfg.Paths = paths
	a.rewire()
	a.ctx = context.Background()
	t0 := time.Now()
	stage := func(name string) {
		t.Logf("════ [%6.1fs] %s", time.Since(t0).Seconds(), name)
	}

	stage("Stage 1 解析+标注")
	if _, err := a.ParseExams(source); err != nil {
		t.Fatalf("解析: %v", err)
	}
	t.Logf("解析完成: %d 题", len(a.lastParse.Questions))

	stage("Stage 2 GNN")
	if _, err := a.PredictWeights(nil); err != nil {
		t.Fatalf("GNN: %v", err)
	}

	stage("Stage 3 CP-SAT")
	bp := a.DefaultBlueprint()
	if os.Getenv("LIVE_BLUEPRINT") == "small" {
		// Reduced-but-complete composition: 10×2 choice + 1×10 major.
		// The gateway is slow (~40s/generate, ~80s/answer-question); a full
		// 150-point paper needs ~2h, which exceeds any sane test timeout.
		bp = orchestrator.Blueprint{
			Subject:    "408",
			TotalScore: 30,
			TypeQuota: map[models.QuestionType]float64{
				models.TypeChoice: 20,
				models.TypeMajor:  10,
			},
			BloomQuota: map[models.CognitiveLevel]int{models.CogApply: 8},
			DiffHist: map[models.DifficultyBand]float64{
				models.BandEasy: 0.3, models.BandMedium: 0.5, models.BandHard: 0.2,
			},
			NumQuestions: 11,
		}
	} else if os.Getenv("LIVE_BLUEPRINT") == "tiny" {
		// Fast smoke run: enough to exercise generation, answer/rubric,
		// validation and assembly without waiting for a full paper.
		bp = orchestrator.Blueprint{
			Subject:    "408",
			TotalScore: 4,
			TypeQuota: map[models.QuestionType]float64{
				models.TypeChoice: 4,
			},
			DiffHist: map[models.DifficultyBand]float64{
				models.BandEasy: 1,
			},
			NumQuestions: 2,
		}
	}
	spec, err := a.ComposeExam(bp, nil)
	if err != nil {
		t.Fatalf("编排: %v", err)
	}
	t.Logf("编排: %d 题位 总分 %.0f", len(spec.Entries), spec.TotalScore)

	stage("Stage 4 逐题生成")
	qs, err := a.GenerateQuestions(*spec)
	if err != nil {
		t.Fatalf("生成: %v", err)
	}

	stage("Stage 5 标准答案")
	reviews, err := a.ProduceAnswers(qs)
	if err != nil {
		t.Fatalf("答案: %v", err)
	}

	stage("Stage 6 校验+修复")
	paper := buildPaper(*spec, qs, reviews)
	paper.ID = fmt.Sprintf("paper_%d", time.Now().UnixMilli())
	maxRepairRounds := 2
	if raw := os.Getenv("LIVE_MAX_REPAIR_ROUNDS"); raw != "" {
		if _, scanErr := fmt.Sscanf(raw, "%d", &maxRepairRounds); scanErr != nil || maxRepairRounds < 0 {
			t.Fatalf("LIVE_MAX_REPAIR_ROUNDS must be a non-negative integer, got %q", raw)
		}
	}
	out, err := a.RepairPaper(paper, maxRepairRounds)
	if err != nil {
		t.Fatalf("校验: %v", err)
	}
	paper = out.Paper
	if v := out.Validation; v != nil {
		t.Logf("校验: coverage=%v difficulty=%v(KL=%.3f) noDup=%v(%d对) score=%v(实际%.0f/期望%.0f) 修复轮次=%d violated=%d",
			v.CoverageOK, v.DifficultyOK, v.KL, v.NoDuplicates, len(v.DuplicatePairs),
			v.ScoreTotalOK, v.ScoreActual, v.ScoreExpected, out.Rounds, len(v.ViolatedSpecIDs))
	}

	stage("Stage 7 装配")
	if _, err := a.AssemblePaper(paper); err != nil {
		t.Fatalf("装配: %v", err)
	}
	// Persist artifacts to the repo so they survive t.TempDir cleanup.
	liveDir := repoPath("output", "live")
	_ = os.MkdirAll(liveDir, 0o755)
	for _, pair := range [][2]string{
		{"exam.tex", a.store.OutputDir + "/exam.tex"},
		{"answers.tex", a.store.OutputDir + "/answers.tex"},
		{"answer_sheet.tex", a.store.OutputDir + "/answer_sheet.tex"},
		{"paper.json", a.store.OutputDir + "/paper.json"},
	} {
		if data, err := os.ReadFile(pair[1]); err == nil {
			_ = os.WriteFile(liveDir+"/"+pair[0], data, 0o644)
		}
	}

	// Quality probes.
	empty, short, badOptions, zeroScore := 0, 0, 0, 0
	for _, q := range paper.Questions {
		if q.Stem == "" {
			empty++
			continue
		}
		if len([]rune(q.Stem)) < 20 {
			short++
		}
		if q.Type == models.TypeChoice && len(q.Options) != 4 {
			badOptions++
		}
		if q.Score <= 0 {
			zeroScore++
		}
	}
	rubricZero := 0
	for _, r := range paper.Rubrics {
		sum := 0.0
		for _, it := range r.Items {
			sum += it.MaxScore
		}
		if sum == 0 {
			rubricZero++
		}
	}
	// Duplicate stems: identical prompts (same template key) can make the
	// LLM emit the same question repeatedly.
	seenStems := make(map[string]int)
	dupStems := 0
	for _, q := range paper.Questions {
		seenStems[q.Stem]++
	}
	for _, n := range seenStems {
		if n > 1 {
			dupStems += n - 1
		}
	}
	t.Logf("质量: 空题面=%d 超短=%d 选项≠4=%d 零分值=%d 零rubric=%d 重复题面=%d",
		empty, short, badOptions, zeroScore, rubricZero, dupStems)
	if empty > 0 {
		t.Errorf("%d 道题空题面 (LLM 生成失败未被替换)", empty)
	}
	if dupStems > 0 {
		t.Errorf("%d 道题题面重复 (内容多样性不足)", dupStems)
	}
	for i, q := range paper.Questions {
		if i >= 3 {
			break
		}
		t.Logf("样例[%d] (%s %.0f分): %.100s", i+1, q.Type, q.Score, q.Stem)
	}

	// PDF compile of the deliverables.
	engine, err := latex.FindEngine()
	if err == nil {
		for _, name := range []string{"exam.tex", "answers.tex", "answer_sheet.tex"} {
			res, cerr := latex.CompileTex(a.ctx, engine, a.store.OutputDir+"/"+name)
			if cerr != nil {
				t.Errorf("编译 %s: %v", name, cerr)
				continue
			}
			t.Logf("✓ %s → %s (%s)", name, res.PDF, res.Duration)
		}
	} else {
		t.Logf("无 TeX 引擎, 跳过 PDF: %v", err)
	}
}
