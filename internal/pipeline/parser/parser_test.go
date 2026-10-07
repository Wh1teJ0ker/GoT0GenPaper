package parser

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

func TestEnglishPartCIsTranslationMajor(t *testing.T) {
	if got := englishQuestionType("english2", "阅读理解", "Part C", models.TypeChoice); got != models.TypeMajor {
		t.Fatalf("english Part C type = %s, want %s", got, models.TypeMajor)
	}
	if got := englishQuestionType("english2", "阅读理解", "Part B", models.TypeChoice); got != models.TypeChoice {
		t.Fatalf("english Part B type = %s, want %s", got, models.TypeChoice)
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
