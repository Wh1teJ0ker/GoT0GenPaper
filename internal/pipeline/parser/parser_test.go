package parser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

func TestAnnotateTaggedQuestionSkipsLLM(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := New(llm.New(server.URL, "test-key"))
	question, err := p.Annotate(context.Background(), models.PastQuestion{
		ID: "408_2024_1", Subject: "408", Type: models.TypeChoice,
		Points: []string{"数据结构", "链表"}, Score: 2, Stem: "测试题面",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("tagged question made %d LLM requests, want 0", got)
	}
	if question.Difficulty != 0.5 || question.DifficultyConfidence != 0.3 || question.Cognitive != models.CogApply {
		t.Errorf("tagged question defaults = difficulty %.2f, confidence %.2f, cognitive %q", question.Difficulty, question.DifficultyConfidence, question.Cognitive)
	}
}

func TestAnnotateMissingTagsUsesConfiguredModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode chat request: %v", err)
		}
		if request.Model != "glm-5.2" {
			t.Errorf("request model = %q, want glm-5.2", request.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{
				"content": `{"points":["数据结构","树"],"cognitive":"analyze","difficulty":0.7,"confidence":0.8}`,
			}}},
		})
	}))
	defer server.Close()

	p := New(llm.New(server.URL, "test-key"))
	p.DefaultModel = "glm-5.2"
	question, err := p.Annotate(context.Background(), models.PastQuestion{
		ID: "408_2024_missing_tag", Subject: "408", Type: models.TypeChoice,
		Score: 2, Stem: "一棵具有 n 个结点的二叉树……",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(question.Points) != 2 || question.Points[1] != "树" || question.Cognitive != models.CogAnalyze {
		t.Errorf("annotated question = points %v, cognitive %q", question.Points, question.Cognitive)
	}
}

func TestEnglishPartCIsTranslationMajor(t *testing.T) {
	if got := englishQuestionType("english2", "阅读理解", "Part C", models.TypeChoice); got != models.TypeMajor {
		t.Fatalf("english Part C type = %s, want %s", got, models.TypeMajor)
	}
	if got := englishQuestionType("english2", "阅读理解", "Part B", models.TypeChoice); got != models.TypeChoice {
		t.Fatalf("english Part B type = %s, want %s", got, models.TypeChoice)
	}
}

func Test4082026SourcePreservesDisciplineFromCategory(t *testing.T) {
	source := filepath.Join("..", "..", "..", "data", "exams", "csgraduates", "408", "2026.md")
	result, err := New(nil).Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]map[models.QuestionType]int)
	for _, question := range result.Questions {
		discipline := models.DisciplineFromPoints(question.Points)
		if counts[discipline] == nil {
			counts[discipline] = make(map[models.QuestionType]int)
		}
		counts[discipline][question.Type]++
	}
	want := map[string]map[models.QuestionType]int{
		"数据结构":  {models.TypeChoice: 11, models.TypeMajor: 2},
		"组成原理":  {models.TypeChoice: 11, models.TypeMajor: 2},
		"操作系统":  {models.TypeChoice: 10, models.TypeMajor: 2},
		"计算机网络": {models.TypeChoice: 8, models.TypeMajor: 1},
	}
	if len(result.Questions) != 47 {
		t.Fatalf("parsed %d 408 questions; want 47", len(result.Questions))
	}
	for discipline, types := range want {
		for questionType, count := range types {
			if counts[discipline][questionType] != count {
				t.Errorf("parsed %s %s count = %d; want %d", discipline, questionType, counts[discipline][questionType], count)
			}
		}
	}
}

func Test408BundledCorpusTagsEveryQuestionWithItsDiscipline(t *testing.T) {
	source := filepath.Join("..", "..", "..", "data", "exams", "csgraduates", "408")
	result, err := New(nil).Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Questions) < 800 {
		t.Fatalf("parsed only %d questions from the 408 corpus", len(result.Questions))
	}
	for _, question := range result.Questions {
		if !models.Is408Discipline(models.DisciplineFromPoints(question.Points)) {
			t.Errorf("question %s has no valid discipline in points %v", question.ID, question.Points)
		}
	}
}

func TestEnglishQuestionPointsPreserveSectionAndPosition(t *testing.T) {
	points := englishQuestionPoints("english2", "阅读理解", "Text 2", 24)
	want := []string{"english2", "阅读理解", "Part A", "Text 2", "题位24"}
	if len(points) != len(want) {
		t.Fatalf("points = %#v, want %#v", points, want)
	}
	for i := range want {
		if points[i] != want[i] {
			t.Errorf("points[%d] = %q, want %q", i, points[i], want[i])
		}
	}
}

func TestEnglish2PartBUsesNamedViewpointPath(t *testing.T) {
	points := englishQuestionPoints("english2", "阅读理解", "Part B", 41)
	want := []string{"english2", "阅读理解", "Part B", "人物观点匹配", "题位41"}
	if strings.Join(points, "/") != strings.Join(want, "/") {
		t.Fatalf("Part B points = %#v, want %#v", points, want)
	}
}

func TestEnglish2Real2026SourceParsesPartBAsNamedViewpoints(t *testing.T) {
	source := filepath.Join("..", "..", "..", "data", "exams", "csgraduates", "english", "english2", "2026.md")
	result, err := New(nil).Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Questions) != 48 {
		t.Fatalf("parsed %d questions, want 48", len(result.Questions))
	}
	seen := 0
	for _, question := range result.Questions {
		position, ok := models.EnglishQuestionPosition(question.Points)
		if ok && position >= 41 && position <= 45 {
			seen++
			if len(question.Points) < 4 || question.Points[3] != "人物观点匹配" {
				t.Fatalf("question %d points = %#v, want named-viewpoint path", position, question.Points)
			}
		}
	}
	if seen != 5 {
		t.Fatalf("found %d Part B questions, want 5", seen)
	}
}

func TestPoliticsKnowledgePointsClassifyBySyllabusModule(t *testing.T) {
	cases := []struct {
		text       string
		wantModule string
		wantTopic  string
	}{
		{"习近平强调大农业观和大食物观，贯彻新发展理念", "习近平新时代中国特色社会主义思想概论", "新发展理念与高质量发展"},
		{"马克思政治经济学认为劳动二重性是理解商品价值的枢纽", "马克思主义基本原理", "马克思主义政治经济学"},
		{"鸦片战争后中国逐步沦为半殖民地半封建社会", "中国近现代史纲要", "近代中国社会与革命"},
		{"法治思维要求坚持法律至上和宪法权威", "思想道德与法治", "法治思维与法律基础"},
	}
	for _, tc := range cases {
		points := politicsKnowledgePoints(tc.text)
		if len(points) < 2 || points[0] != tc.wantModule || points[1] != tc.wantTopic {
			t.Errorf("politicsKnowledgePoints(%q) = %#v, want [%q %q]", tc.text, points, tc.wantModule, tc.wantTopic)
		}
	}
}

func TestPoliticsParseAnnotatesWithoutLLMAndUsesSyllabusPoints(t *testing.T) {
	source := filepath.Join("..", "..", "..", "data", "exams", "csgraduates", "politics", "2025.md")
	result, err := New(nil).Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Questions) < 30 {
		t.Fatalf("parsed %d politics questions, want a full source paper", len(result.Questions))
	}
	for _, question := range result.Questions {
		if !isPoliticsSubject(question.Subject) || len(question.Points) < 2 {
			t.Errorf("question %s has no politics syllabus path: subject=%q points=%v", question.ID, question.Subject, question.Points)
		}
	}
}

func TestPoliticsCorpusProvidesEnoughDistinctPointSetsForPaper(t *testing.T) {
	source := filepath.Join("..", "..", "..", "data", "exams", "csgraduates", "politics")
	result, err := New(nil).Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	uniqueByType := map[models.QuestionType]map[string]struct{}{}
	modules := map[string]struct{}{}
	for _, question := range result.Questions {
		if len(question.Points) < 2 {
			t.Fatalf("question %s has incomplete political points: %v", question.ID, question.Points)
		}
		if uniqueByType[question.Type] == nil {
			uniqueByType[question.Type] = map[string]struct{}{}
		}
		uniqueByType[question.Type][strings.Join(question.Points, "/")] = struct{}{}
		modules[question.Points[0]] = struct{}{}
	}
	for questionType, minimum := range map[models.QuestionType]int{
		models.TypeChoice: 16, models.TypeMultiChoice: 17, models.TypeMajor: 5,
	} {
		if got := len(uniqueByType[questionType]); got < minimum {
			t.Errorf("politics %s has %d distinct point sets; need at least %d for a full paper", questionType, got, minimum)
		}
	}
	if len(modules) != 6 {
		t.Errorf("politics corpus classified into %d syllabus modules, want all six: %v", len(modules), modules)
	}
}
