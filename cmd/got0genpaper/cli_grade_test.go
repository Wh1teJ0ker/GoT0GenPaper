package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"GoT0GenPaper/internal/models"
)

func TestGradeCLIReportsMissingDuplicateAndUnknownAnswers(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("GOT0GENPAPER_CONFIG", filepath.Join(root, "config.toml"))

	paper := models.ExamPaper{
		Questions: []models.GeneratedQuestion{
			{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"},
			{ID: "q2", Type: models.TypeMajor, Score: 8},
		},
		Rubrics: []models.Rubric{
			{QuestionID: "q1", Items: []models.RubricItem{{ID: "q1-cp", MaxScore: 2}}},
			{QuestionID: "q2", Items: []models.RubricItem{{ID: "q2-cp", MaxScore: 8}}},
		},
	}
	paperPath := filepath.Join(root, "paper.json")
	answersPath := filepath.Join(root, "answers.json")
	outputPath := filepath.Join(root, "grades.json")
	writeJSONForTest(t, paperPath, paper)
	writeJSONForTest(t, answersPath, []map[string]interface{}{
		{"questionId": "q1", "answerStatus": "recognized", "questionType": "choice", "studentOptions": []string{"A"}},
		{"questionId": "q1", "answerStatus": "recognized", "questionType": "choice", "studentOptions": []string{"B"}},
		{"questionId": "unknown", "studentAnswer": "内容"},
	})

	if err := runCLI([]string{"grade", "--paper", paperPath, "--answers", answersPath, "--output", outputPath}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Results     []struct{ QuestionID, Status string } `json:"results"`
		NeedsReview []string                              `json:"needsReview"`
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 3 {
		t.Fatalf("results = %+v", report.Results)
	}
	if report.Results[0].QuestionID != "q1" || report.Results[0].Status != "needs_review" {
		t.Fatalf("duplicate result = %+v", report.Results[0])
	}
	if report.Results[1].QuestionID != "q2" || report.Results[1].Status != "blank" {
		t.Fatalf("missing result = %+v", report.Results[1])
	}
	if report.Results[2].QuestionID != "unknown" || report.Results[2].Status != "needs_review" {
		t.Fatalf("unknown result = %+v", report.Results[2])
	}
}

func writeJSONForTest(t *testing.T, path string, value interface{}) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
