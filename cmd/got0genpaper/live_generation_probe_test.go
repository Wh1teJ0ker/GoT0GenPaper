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
	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.ActiveProvider()
	if provider == nil {
		t.Fatal("no active provider")
	}
	a := &API{cfg: cfg}
	a.rewire()
	gen := generator.New(a.llmC, nil)
	gen.ModelByType = provider.ModelByType
	gen.DefaultModel = provider.DefaultModel
	gen.SetFewShot(nil)
	spec := models.SpecEntry{
		ID: "probe_001", Type: models.TypeChoice, Points: []string{"高等数学", "一元函数微分学", "复合函数求导"},
		Cognitive: models.CogApply, DiffBand: models.BandMedium, Score: 5,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Second)
	defer cancel()
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
}
