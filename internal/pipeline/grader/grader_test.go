package grader

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

func testRubric(score float64) models.Rubric {
	return models.Rubric{QuestionID: "q1", Items: []models.RubricItem{{ID: "cp1", Description: "选择正确", MaxScore: score}}}
}

func TestObjectiveGradingIsDeterministic(t *testing.T) {
	g := New(nil, "")
	got, err := g.Grade(context.Background(), GradeRequest{QuestionID: "q1", QuestionType: "choice", CorrectAnswer: "答案为 B", StudentOptions: []string{"B"}, AnswerStatus: "recognized", Confidence: 1}, testRubric(2))
	if err != nil || got.Status != "graded" || got.TotalScore != 2 || !got.AutoScored {
		t.Fatalf("correct = %+v, %v", got, err)
	}

	got, err = g.Grade(context.Background(), GradeRequest{QuestionID: "q1", QuestionType: "choice", CorrectAnswer: "答案为 B", StudentOptions: []string{"A"}, AnswerStatus: "recognized", Confidence: 1}, testRubric(2))
	if err != nil || got.Status != "graded" || got.TotalScore != 0 {
		t.Fatalf("wrong = %+v, %v", got, err)
	}

	got, err = g.Grade(context.Background(), GradeRequest{QuestionID: "q1", QuestionType: "choice", CorrectAnswer: "答案为 B", StudentOptions: []string{"A", "B"}, AnswerStatus: "ambiguous", Confidence: 1}, testRubric(2))
	if err != nil || got.Status != "needs_review" || got.AutoScored {
		t.Fatalf("ambiguous = %+v, %v", got, err)
	}
}

func TestObjectiveReviewStatusIsNotDowngradedToBlank(t *testing.T) {
	g := New(nil, "")
	for _, status := range []string{"ambiguous", "needs_review"} {
		got, err := g.Grade(context.Background(), GradeRequest{
			QuestionID: "q1", QuestionType: "choice", AnswerStatus: status,
		}, testRubric(2))
		if err != nil || got.Status != "needs_review" || got.AutoScored {
			t.Fatalf("status %q became an unsafe result: %+v, %v", status, got, err)
		}
	}
}

func TestNonEmptyAnswerNeverGetsOfflineFullMarks(t *testing.T) {
	g := New(nil, "")
	got, err := g.Grade(context.Background(), GradeRequest{QuestionID: "q1", StudentAnswer: "一个非空答案"}, testRubric(10))
	if err != nil || got.TotalScore != 0 || got.Status != "needs_review" || got.AutoScored {
		t.Fatalf("fallback = %+v, %v", got, err)
	}
}

func TestInvalidRubricIsNeverAutoScored(t *testing.T) {
	g := New(nil, "")
	for _, rubric := range []models.Rubric{
		{QuestionID: "q1", Items: []models.RubricItem{{ID: "", MaxScore: 2}}},
		{QuestionID: "q1", Items: []models.RubricItem{{ID: "cp1", MaxScore: math.NaN()}}},
		{QuestionID: "q1", Items: []models.RubricItem{{ID: "cp1", MaxScore: 2}, {ID: "cp1", MaxScore: 2}}},
	} {
		got, err := g.Grade(context.Background(), GradeRequest{QuestionID: "q1", StudentAnswer: "答案"}, rubric)
		if err != nil || got.Status != "needs_review" || got.AutoScored {
			t.Fatalf("invalid rubric result = %+v, err=%v", got, err)
		}
	}
}

func TestSingleChoiceMultipleOptionsNeedsReview(t *testing.T) {
	g := New(nil, "")
	got, err := g.Grade(context.Background(), GradeRequest{
		QuestionID: "q1", QuestionType: "choice", CorrectAnswer: "答案为 A", StudentOptions: []string{"A", "B"}, AnswerStatus: "recognized",
	}, testRubric(2))
	if err != nil || got.Status != "needs_review" || got.AutoScored {
		t.Fatalf("multiple single-choice options = %+v, err=%v", got, err)
	}
}

func TestLLMOutOfRangePartialScoreNeedsReview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checkpoints\":[{\"rubricItemId\":\"cp1\",\"passed\":true,\"partialScore\":99,\"note\":\"bad\"}]}"}}]}`))
	}))
	defer server.Close()

	g := New(llmForTest(server.URL), "model")
	got, err := g.Grade(context.Background(), GradeRequest{QuestionID: "q1", StudentAnswer: "答案"}, testRubric(2))
	if err != nil || got.Status != "needs_review" || got.AutoScored {
		t.Fatalf("out-of-range score = %+v, err=%v", got, err)
	}
}

func llmForTest(baseURL string) *llm.Client {
	return llm.New(baseURL, "test-key")
}

func TestGradeAllReportsMissingDuplicateAndUnknownSubmissions(t *testing.T) {
	g := New(nil, "")
	rubrics := []models.Rubric{
		testRubric(2),
		{QuestionID: "q2", Items: []models.RubricItem{{ID: "cp2", MaxScore: 2}}},
	}
	results, err := g.GradeAll(context.Background(), []GradeRequest{
		{QuestionID: "q1", QuestionType: "choice", CorrectAnswer: "答案为 A", StudentOptions: []string{"A"}, AnswerStatus: "recognized", Confidence: 1},
		{QuestionID: "q1", QuestionType: "choice", CorrectAnswer: "答案为 A", StudentOptions: []string{"B"}, AnswerStatus: "recognized", Confidence: 1},
		{QuestionID: "q-unknown", StudentAnswer: "内容"},
	}, rubrics)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].QuestionID != "q1" || results[0].Status != "needs_review" || results[0].AutoScored {
		t.Fatalf("duplicate = %+v", results[0])
	}
	if results[1].QuestionID != "q2" || results[1].Status != "needs_review" {
		t.Fatalf("missing = %+v", results[1])
	}
	if results[2].QuestionID != "q-unknown" || results[2].Status != "needs_review" {
		t.Fatalf("unknown = %+v", results[2])
	}
}
