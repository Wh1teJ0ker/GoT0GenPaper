package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"GoT0GenPaper/internal/config"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/generator"
)

func TestLiveGenerationProbe(t *testing.T) {
	if os.Getenv("LIVE_GENERATION_PROBE") != "1" {
		t.Skip("set LIVE_GENERATION_PROBE=1")
	}
	// go test runs this package with cmd/got0genpaper as cwd. Resolve the
	// repository config explicitly so the probe tests the provider selected in
	// the working tree rather than silently falling back to AppData defaults.
	cfg, err := config.Load(repoPath("config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.ActiveProvider()
	if provider == nil {
		t.Fatal("no active provider")
	}
	a := &app{cfg: cfg}
	a.rewire()
	gen := generator.New(a.llmC, nil)
	gen.ModelByType = provider.ModelByType
	gen.DefaultModel = provider.DefaultModel
	gen.SetFewShot(nil)
	probes := []models.SpecEntry{
		{ID: "probe_408_choice", Type: models.TypeChoice, Points: []string{"408", "数据结构", "图的遍历", "BFS与DFS"}, Cognitive: models.CogApply, DiffBand: models.BandMedium, Score: 2},
		{ID: "probe_408_major", Type: models.TypeMajor, Points: []string{"408", "数据结构", "图的遍历", "复杂度分析"}, Cognitive: models.CogAnalyze, DiffBand: models.BandMedium, Score: 10},
		{ID: "probe_english1_choice", Type: models.TypeChoice, Points: []string{"英语一", "英语知识运用", "词汇与语法"}, Cognitive: models.CogAnalyze, DiffBand: models.BandMedium, Score: 0.5},
		{ID: "probe_english1_major", Type: models.TypeMajor, Points: []string{"英语一", "英译汉", "翻译"}, Cognitive: models.CogAnalyze, DiffBand: models.BandMedium, Score: 10},
		{ID: "probe_english2_choice", Type: models.TypeChoice, Points: []string{"英语二", "阅读理解", "主旨推断"}, Cognitive: models.CogAnalyze, DiffBand: models.BandMedium, Score: 2},
		{ID: "probe_english2_major", Type: models.TypeMajor, Points: []string{"英语二", "英译汉", "翻译"}, Cognitive: models.CogAnalyze, DiffBand: models.BandMedium, Score: 15},
		{ID: "probe_politics_choice", Type: models.TypeChoice, Points: []string{"政治", "毛泽东思想和中国特色社会主义理论体系概论", "新发展理念"}, Cognitive: models.CogAnalyze, DiffBand: models.BandMedium, Score: 1},
		{ID: "probe_politics_multi", Type: models.TypeMultiChoice, Points: []string{"政治", "马克思主义基本原理", "矛盾分析法"}, Cognitive: models.CogAnalyze, DiffBand: models.BandMedium, Score: 2},
		{ID: "probe_politics_major", Type: models.TypeMajor, Points: []string{"政治", "毛泽东思想和中国特色社会主义理论体系概论", "新发展理念"}, Cognitive: models.CogAnalyze, DiffBand: models.BandMedium, Score: 10},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	for _, spec := range probes {
		spec := spec
		t.Run(spec.ID, func(t *testing.T) {
			started := time.Now()
			question, err := gen.Generate(ctx, spec)
			t.Logf("elapsed=%s status=%s options=%d stemRunes=%d answerRunes=%d warnings=%v", time.Since(started).Round(time.Millisecond), question.Status, len(question.Options), len([]rune(question.Stem)), len([]rune(question.Answer)), question.Warnings)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(question)
			t.Logf("result=%s", data)
			if question.Status != "ready" {
				t.Fatalf("probe question not ready: %v", question.Warnings)
			}
		})
	}
}
