package validator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
)

func TestValidateRejectsEmptyPaper(t *testing.T) {
	result, err := New(nil).Validate(context.Background(), models.ExamPaper{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StructureOK || result.ContentReady || result.CoverageOK || result.DifficultyOK || result.ScoreTotalOK {
		t.Fatalf("empty paper passed validation: %+v", result)
	}
	if len(result.StructureIssues) == 0 {
		t.Fatal("empty paper did not report a structural issue")
	}
}

func TestValidateRejectsInvalidQuestionShape(t *testing.T) {
	paper := models.ExamPaper{
		Questions: []models.GeneratedQuestion{
			{ID: "q1", Type: models.TypeChoice, Score: 0},
			{ID: "q1", Type: models.QuestionType("unknown"), Score: 2},
		},
		TotalScore: 2,
	}
	result, err := New(nil).Validate(context.Background(), paper, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StructureOK || len(result.StructureIssues) < 3 {
		t.Fatalf("invalid question shape was not rejected: %+v", result)
	}
}

func TestMissingCoverageMapsBackToSpecSlot(t *testing.T) {
	bp := &orchestrator.Blueprint{
		TotalScore:    10,
		CoverageFloor: map[string]float64{"missing": 1},
		DiffHist:      map[models.DifficultyBand]float64{models.BandMedium: 1},
	}
	paper := models.ExamPaper{
		Questions:  []models.GeneratedQuestion{{ID: "q1", SpecID: "spec_001", Type: models.TypeMajor, Score: 10, Difficulty: 0.5, Points: []string{"known"}}},
		SpecTable:  models.SpecTable{Entries: []models.SpecEntry{{ID: "spec_002", Points: []string{"missing"}}}},
		TotalScore: 10,
	}
	result, err := New(nil).Validate(context.Background(), paper, bp)
	if err != nil {
		t.Fatal(err)
	}
	if result.CoverageOK || len(result.MissingPoints) != 1 || result.MissingPoints[0] != "missing" {
		t.Fatalf("coverage result = %+v", result)
	}
	if !contains(result.ViolatedSpecIDs, "spec_002") {
		t.Fatalf("missing point was not mapped to spec slot: %v", result.ViolatedSpecIDs)
	}
}

func TestValidateRequiresCompleteRubrics(t *testing.T) {
	paper := models.ExamPaper{
		Questions:  []models.GeneratedQuestion{{ID: "q1", Type: models.TypeMajor, Score: 10, Stem: "题面", Answer: "答案", Status: "ready"}},
		TotalScore: 10,
	}
	result, err := New(nil).Validate(context.Background(), paper, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentReady {
		t.Fatalf("paper without rubric passed content validation: %+v", result)
	}
	if len(result.ContentIssues) == 0 {
		t.Fatal("missing rubric was not reported")
	}
}

func TestValidateChecksDisciplineQuotasAndOrder(t *testing.T) {
	bp := &orchestrator.Blueprint{
		TotalScore: 10,
		TypeQuota: map[models.QuestionType]float64{
			models.TypeChoice: 10,
		},
		TypeCountQuota: map[models.QuestionType]int{
			models.TypeChoice: 2,
		},
		DisciplineQuota: map[string]map[models.QuestionType]float64{
			"高等数学": {models.TypeChoice: 5},
			"线性代数": {models.TypeChoice: 5},
		},
		DisciplineCountQuota: map[string]map[models.QuestionType]int{
			"高等数学": {models.TypeChoice: 1},
			"线性代数": {models.TypeChoice: 1},
		},
		DisciplineOrder: []string{"高等数学", "线性代数"},
	}
	makePaper := func(points ...[]string) models.ExamPaper {
		paper := models.ExamPaper{TotalScore: 10}
		for i, point := range points {
			paper.Questions = append(paper.Questions, models.GeneratedQuestion{
				ID: fmt.Sprintf("q%d", i+1), Type: models.TypeChoice, Score: 5,
				Difficulty: 0.5, Stem: fmt.Sprintf("question %d", i+1), Answer: "A",
				Options: []string{"a", "b", "c", "d"}, Points: point, Status: "ready",
			})
		}
		return paper
	}
	check := func(name string, paper models.ExamPaper, wantStructureOK bool, wantIssue string) {
		t.Helper()
		result, err := New(nil).Validate(context.Background(), paper, bp)
		if err != nil {
			t.Fatalf("%s: Validate returned error: %v", name, err)
		}
		if result.StructureOK != wantStructureOK {
			t.Errorf("%s: StructureOK = %v, want %v; issues=%v", name, result.StructureOK, wantStructureOK, result.StructureIssues)
		}
		if wantIssue != "" {
			found := false
			for _, issue := range result.StructureIssues {
				found = found || strings.Contains(issue, wantIssue)
			}
			if !found {
				t.Errorf("%s: missing issue containing %q: %v", name, wantIssue, result.StructureIssues)
			}
		}
	}

	check("balanced and ordered", makePaper([]string{"高等数学", "导数"}, []string{"线性代数", "矩阵"}), true, "")
	check("wrong balance", makePaper([]string{"高等数学", "导数"}, []string{"高等数学", "积分"}), false, "线性代数 的 choice 数量为 0")
	check("wrong order", makePaper([]string{"线性代数", "矩阵"}, []string{"高等数学", "导数"}), false, "学科顺序不符合蓝图")
	check("ODE is calculus", makePaper([]string{"常微分方程", "方程"}, []string{"线性代数", "矩阵"}), true, "")
}
