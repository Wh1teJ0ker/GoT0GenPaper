package main

import (
	"testing"

	"GoT0GenPaper/internal/models"
)

func TestNormalizeEnglishPaperOrderCanonicalizesEnglish2PartB(t *testing.T) {
	paper := &models.ExamPaper{
		Subject: "英语二",
		Questions: []models.GeneratedQuestion{{
			ID: "q41", SpecID: "spec_041",
			Points: []string{"english2", "阅读理解", "Part B", "观点匹配", "题位41"},
		}},
		SpecTable: models.SpecTable{Entries: []models.SpecEntry{{ID: "spec_041"}}},
	}
	if err := normalizeEnglishPaperOrder(paper); err != nil {
		t.Fatal(err)
	}
	if got := paper.Questions[0].Points[3]; got != "人物观点匹配" {
		t.Fatalf("Part B label = %q, want 人物观点匹配", got)
	}
}
