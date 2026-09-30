package generator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

func choiceSpec(kind models.QuestionType) models.SpecEntry {
	return models.SpecEntry{ID: "slot-1", Type: kind, Score: 2, DiffBand: models.BandMedium}
}

func TestFinishValidatesChoiceAnswerShape(t *testing.T) {
	g := New(nil, nil)
	q, err := g.finish(choiceSpec(models.TypeChoice), generationResponse{
		Stem:       "题面",
		Options:    []string{"甲", "乙", "丙", "丁"},
		Answer:     "答案为 B",
		Difficulty: .5,
	})
	if err != nil || q.Status != "ready" || len(q.Warnings) != 0 {
		t.Fatalf("valid choice = %+v, err=%v", q, err)
	}
}

func TestFinishMarksMalformedChoiceForReview(t *testing.T) {
	g := New(nil, nil)
	q, err := g.finish(choiceSpec(models.TypeChoice), generationResponse{
		Stem:       "题面",
		Options:    []string{"甲", "乙", "丙"},
		Answer:     "参考答案无法确定",
		Difficulty: .5,
	})
	if err != nil || q.Status != "needs_review" || len(q.Warnings) == 0 {
		t.Fatalf("malformed choice = %+v, err=%v", q, err)
	}
}

func TestGenerateRejectsInvalidSpec(t *testing.T) {
	g := New(nil, nil)
	if _, err := g.Generate(nil, models.SpecEntry{ID: "", Type: models.TypeChoice, Score: 2}); err == nil {
		t.Fatal("empty spec ID should fail")
	}
	if _, err := g.Generate(nil, models.SpecEntry{ID: "slot-1", Type: models.TypeChoice}); err == nil {
		t.Fatal("non-positive score should fail")
	}
}

func TestGenerateRetriesOnceWhenChoiceShapeIsInvalid(t *testing.T) {
	var calls int
	var firstRequest llm.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls++
		if calls == 1 {
			firstRequest = request
		}
		content := `{"stem":"计算题，结果为____","options":[],"answer":"1","difficulty":0.5}`
		if calls == 2 {
			content = `{"stem":"设函数 $f(x)=x^2$，则 $f'(1)$ 等于（ ）","options":["$0$","$1$","$2$","$3$"],"answer":"答案：C。因为 $f'(x)=2x$，所以 $f'(1)=2。","difficulty":0.5}`
		}
		body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
		if err != nil {
			t.Errorf("marshal response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := llm.New(srv.URL, "test-key")
	client.ReasoningEffort = "low"
	client.MaxTokens = 4096
	g := New(client, nil)
	g.ModelByType["choice"] = "glm-5.2"
	q, err := g.Generate(context.Background(), choiceSpec(models.TypeChoice))
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != "ready" {
		t.Fatalf("question status = %q, warnings=%v", q.Status, q.Warnings)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want first generation plus one format retry", calls)
	}
	if firstRequest.ReasoningEffort != "low" || firstRequest.MaxTokens != 4096 {
		t.Errorf("generation controls = effort %q, max_tokens %d", firstRequest.ReasoningEffort, firstRequest.MaxTokens)
	}
	if !strings.Contains(firstRequest.Messages[0].Content.(string), "禁止生成填空题") {
		t.Fatal("choice prompt does not explicitly forbid fill-in questions")
	}
}
