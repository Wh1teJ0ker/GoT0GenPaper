package answer

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

func TestNormalizeRubricMatchesQuestionScore(t *testing.T) {
	items := normalizeRubricItems([]models.RubricItem{{ID: "a", MaxScore: 2}, {ID: "b", MaxScore: 8}}, 5)
	if len(items) != 2 || items[0].MaxScore+items[1].MaxScore != 5 {
		t.Fatalf("items = %+v", items)
	}
	if got := fallbackRubricItems(2); len(got) != 1 || got[0].MaxScore != 2 {
		t.Fatalf("fallback = %+v", got)
	}
}

func TestNormalizeRubricRejectsNonFiniteValuesAndRepairsIDs(t *testing.T) {
	items := normalizeRubricItems([]models.RubricItem{
		{ID: " chk_2 ", MaxScore: 1},
		{ID: "chk_2", MaxScore: 1},
		{ID: "", MaxScore: math.Inf(1)},
		{ID: "", MaxScore: 2},
	}, math.NaN())
	if len(items) != 3 {
		t.Fatalf("items = %+v, want three finite items", items)
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.ID == "" || seen[item.ID] || !finitePositive(item.MaxScore) || math.IsNaN(item.MaxScore) || math.IsInf(item.MaxScore, 0) {
			t.Fatalf("invalid normalized item = %+v", item)
		}
		seen[item.ID] = true
	}
	if got := fallbackRubricItems(math.Inf(1))[0].MaxScore; got != 10 {
		t.Fatalf("infinite fallback score = %v, want 10", got)
	}
}

func TestScoreCheckpointsRejectsMalformedHTTPResponses(t *testing.T) {
	rubric := models.Rubric{Items: []models.RubricItem{{ID: "cp1", MaxScore: 2}, {ID: "cp2", MaxScore: 3}}}
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{name: "unknown", content: `{"verdicts":[{"rubricItemId":"cp1","passed":true,"confidence":0.9},{"rubricItemId":"other","passed":true,"confidence":0.9}]}`, want: "未知"},
		{name: "duplicate", content: `{"verdicts":[{"rubricItemId":"cp1","passed":true,"confidence":0.9},{"rubricItemId":"cp1","passed":true,"confidence":0.9}]}`, want: "重复"},
		{name: "missing", content: `{"verdicts":[{"rubricItemId":"cp1","passed":true,"confidence":0.9}]}`, want: "未覆盖"},
		{name: "invalid-confidence", content: `{"verdicts":[{"rubricItemId":"cp1","passed":true,"confidence":2},{"rubricItemId":"cp2","passed":true,"confidence":0.9}]}`, want: "无效置信度"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + quoteJSON(tc.content) + `}}]}`))
			}))
			defer server.Close()

			s := New(llm.New(server.URL, "test-key"), "model-a", "", "")
			verdicts, err := s.scoreCheckpoints(context.Background(), s.Agent1, models.GeneratedQuestion{Stem: "题目", Answer: "答案"}, rubric)
			if err != nil {
				t.Fatal(err)
			}
			if len(verdicts) != len(rubric.Items) {
				t.Fatalf("verdicts = %+v", verdicts)
			}
			for _, verdict := range verdicts {
				if verdict.Confidence != 0 || !strings.Contains(verdict.Note, tc.want) {
					t.Fatalf("verdict = %+v, want unresolved %q", verdict, tc.want)
				}
			}
		})
	}
}

func quoteJSON(value string) string {
	data := []byte(value)
	quoted := make([]byte, 0, len(data)+2)
	quoted = append(quoted, '"')
	for _, ch := range data {
		switch ch {
		case '\\':
			quoted = append(quoted, '\\', '\\')
		case '"':
			quoted = append(quoted, '\\', '"')
		case '\n':
			quoted = append(quoted, '\\', 'n')
		default:
			quoted = append(quoted, ch)
		}
	}
	quoted = append(quoted, '"')
	return string(quoted)
}

func TestAnswerFallbackIsNotIndependentlyVerified(t *testing.T) {
	s := New(nil, "", "", "")
	result, err := s.ProduceAnswer(nil, models.GeneratedQuestion{ID: "q1", Type: models.TypeMajor, Score: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.SympyVerified || result.Finalised {
		t.Fatalf("unverified answer = %+v", result)
	}
}

func TestChoiceAnswerFallbackIsNotFinalisedWithoutIndependentEvidence(t *testing.T) {
	s := New(nil, "", "", "")
	result, err := s.ProduceAnswer(nil, models.GeneratedQuestion{ID: "q1", Type: models.TypeChoice, Score: 2, Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Finalised || len(result.Verdicts) == 0 || result.Verdicts[0].Confidence != 0 {
		t.Fatalf("unverified choice answer = %+v", result)
	}
}
