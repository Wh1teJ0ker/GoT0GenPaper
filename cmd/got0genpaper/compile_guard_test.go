package main

import (
	"os"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

func TestCompilePaperPDFRejectsUnreviewedPaperBeforeTeX(t *testing.T) {
	a := newTestApp(t)
	paper := models.ExamPaper{
		Questions: []models.GeneratedQuestion{{
			ID: "q1", Type: models.TypeMajor, Score: 10,
			Stem: "% TODO: 生成题面", Answer: "% TODO: 参考答案", Status: "needs_review",
		}},
		TotalScore: 10,
	}
	if err := a.store.SaveOutputJSON("paper", paper); err != nil {
		t.Fatal(err)
	}
	_, err := a.compilePaperPDF()
	if err == nil || !strings.Contains(err.Error(), "needs_review") {
		t.Fatalf("compile guard error = %v", err)
	}
}

func TestAssemblePaperWritesOnlyExamArtifact(t *testing.T) {
	a := newTestApp(t)
	paper := models.ExamPaper{
		Subject: "408",
		Questions: []models.GeneratedQuestion{{
			ID: "q1", Type: models.TypeMajor, Score: 10,
			Stem: "有效题面", Answer: "根据题设中的条件建立方程，逐步代入并化简得到中间结果，再将结果代回原条件检验，最后得到与题意一致的明确结论。", Status: "ready",
		}},
		TotalScore: 10,
	}
	if _, err := a.assemblePaper(paper); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(a.store.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("generated %d output files, want only paper.json and exam.tex: %v", len(entries), entries)
	}
	for i, name := range []string{"exam.tex", "paper.json"} {
		if entries[i].Name() != name {
			t.Errorf("output[%d] = %q, want %q", i, entries[i].Name(), name)
		}
	}
}
