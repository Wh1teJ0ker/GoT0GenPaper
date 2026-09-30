package main

import (
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

func TestCompilePaperPDFRejectsUnreviewedPaperBeforeTeX(t *testing.T) {
	a := newTestAPI(t)
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
	_, err := a.CompilePaperPDF()
	if err == nil || !strings.Contains(err.Error(), "needs_review") {
		t.Fatalf("compile guard error = %v", err)
	}
}

func TestCompilePaperPDFRejectsMissingRubric(t *testing.T) {
	a := newTestAPI(t)
	paper := models.ExamPaper{
		Questions: []models.GeneratedQuestion{{
			ID: "q1", Type: models.TypeMajor, Score: 10,
			Stem: "有效题面", Answer: "有效答案", Status: "ready",
		}},
		TotalScore: 10,
	}
	if err := a.store.SaveOutputJSON("paper", paper); err != nil {
		t.Fatal(err)
	}
	_, err := a.CompilePaperPDF()
	if err == nil || !strings.Contains(err.Error(), "缺少评分标准") {
		t.Fatalf("missing rubric compile error = %v", err)
	}
}
