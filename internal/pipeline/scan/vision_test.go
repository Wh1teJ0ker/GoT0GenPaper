package scan

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

func TestGradeScanWithVisionSendsSegmentImageAndValidatesResult(t *testing.T) {
	segmentPath := filepath.Join(t.TempDir(), "q1.png")
	f, err := os.Create(segmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewGray(image.Rect(0, 0, 32, 32))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	var gotImage bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for _, message := range request.Messages {
			parts, ok := message.Content.([]any)
			if !ok {
				continue
			}
			for _, raw := range parts {
				part, ok := raw.(map[string]any)
				if !ok || part["type"] != "image_url" {
					continue
				}
				imageURL, ok := part["image_url"].(map[string]any)
				if ok && strings.HasPrefix(imageURL["url"].(string), "data:image/png;base64,") {
					gotImage = true
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"questionId\":\"q1\",\"status\":\"graded\",\"selected\":[\"A\"],\"score\":2,\"confidence\":0.95,\"note\":\"识别清晰\"}"}}]}`))
	}))
	defer server.Close()

	paper := models.ExamPaper{ID: "paper-vision", Questions: []models.GeneratedQuestion{{
		ID: "q1", Type: models.TypeChoice, Options: []string{"A", "B", "C", "D"}, Answer: "答案为 A", Score: 2,
	}}}
	report := GradeScanWithVision(context.Background(), paper, ScanResult{
		PaperID:          paper.ID,
		PaperFingerprint: paperFingerprint(paper),
		Segments:         []AnswerSegment{{QuestionID: "q1", Number: 1, Status: StatusRecognized, ImagePath: segmentPath}},
	}, llm.New(server.URL, "test-key"), "vision-model")
	if !gotImage {
		t.Fatal("vision request did not contain a PNG image_url part")
	}
	if report.Mode != "segmented-vision" || report.TotalScore != 2 || len(report.Results) != 1 || report.Results[0].Status != "graded" {
		t.Fatalf("vision report = %+v", report)
	}
}

func TestGradeScanWithVisionKeepsInvalidModelResultInReview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"questionId\":\"wrong\",\"status\":\"graded\",\"score\":2,\"confidence\":0.99}"}}]}`))
	}))
	defer server.Close()
	segmentPath := filepath.Join(t.TempDir(), "q1.png")
	f, err := os.Create(segmentPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = png.Encode(f, image.NewGray(image.Rect(0, 0, 16, 16)))
	_ = f.Close()
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeMajor, Score: 10}}}
	report := GradeScanWithVision(context.Background(), paper, ScanResult{Segments: []AnswerSegment{{QuestionID: "q1", Number: 1, Status: StatusRecognized, ImagePath: segmentPath}}}, llm.New(server.URL, "key"), "vision-model")
	if report.TotalScore != 0 || len(report.NeedsReview) != 1 || report.Results[0].Status != StatusNeedsReview {
		t.Fatalf("invalid vision result was accepted: %+v", report)
	}
}
