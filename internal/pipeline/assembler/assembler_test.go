package assembler

import (
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

func TestEnglishPaperUsesOfficialSectionTitles(t *testing.T) {
	paper := models.ExamPaper{
		Subject: "英语二",
		Questions: []models.GeneratedQuestion{
			{ID: "cloze", Type: models.TypeChoice, Score: 0.5, Points: []string{"english2", "完形填空", "题位1"}, Stem: "cloze", Options: []string{"A", "B", "C", "D"}},
			{ID: "reading", Type: models.TypeChoice, Score: 2, Points: []string{"english2", "阅读理解", "Part A", "Text 1", "题位21"}, Stem: "reading", Options: []string{"A", "B", "C", "D"}},
			{ID: "translation", Type: models.TypeMajor, Score: 15, Points: []string{"english2", "翻译", "英译汉", "题位46"}, Stem: "translation"},
		},
	}
	groups := groupSections(paper)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3: %+v", len(groups), groups)
	}
	want := []string{"一、完形填空", "二、阅读理解（Part A）", "三、翻译（Part C）"}
	for i, title := range want {
		if groups[i].Title != title {
			t.Fatalf("group %d title = %q, want %q", i, groups[i].Title, title)
		}
	}
}

func TestEnglish2RenderingPlacesSharedMaterialAndPartBStatementsOnce(t *testing.T) {
	shared := strings.Repeat("People use evidence to improve local services and build useful institutions. ", 4) + "Ramni Jannadass identifies a supply bottleneck. Christopher Kettle explains why infrastructure matters. John Stanturf warns about fast-multiplying threats. Pedro Brancalion compares global goals with local needs. Robin Chazdon stresses that restoration is about people as well as trees."
	headings := []string{"Seed supply is a central bottleneck", "Infrastructure must support restoration", "Fast-multiplying threats require attention", "Local needs may differ from global goals", "People matter as much as trees", "Private funding solves every problem", "Restoration needs no local capacity"}
	paper := models.ExamPaper{
		Subject: "英语二",
		Questions: []models.GeneratedQuestion{
			{ID: "q41", Type: models.TypeChoice, Score: 2, GroupID: "english2-part-b", SharedMaterial: shared, SharedOptions: headings, Options: headings, Points: []string{"english2", "阅读理解", "Part B", "人物观点匹配", "题位41"}, Stem: "Ramni Jannadass", Answer: "答案：A。"},
			{ID: "q42", Type: models.TypeChoice, Score: 2, GroupID: "english2-part-b", SharedMaterial: shared, SharedOptions: headings, Options: headings, Points: []string{"english2", "阅读理解", "Part B", "人物观点匹配", "题位42"}, Stem: "Christopher Kettle", Answer: "答案：B。"},
		},
	}
	rendered := New().renderExam(paper)
	if strings.Count(rendered, shared) != 1 {
		t.Fatalf("shared passage rendered %d times", strings.Count(rendered, shared))
	}
	if strings.Count(rendered, "Restoration needs no local capacity") != 1 {
		t.Fatalf("shared Part B statement rendered %d times", strings.Count(rendered, "Restoration needs no local capacity"))
	}
	if strings.Count(rendered, "Ramni Jannadass") < 2 || strings.Count(rendered, "Christopher Kettle") < 2 {
		t.Fatal("Part B named-person prompts were not rendered")
	}
	for _, want := range []string{"numbered name", "There are two extra statements", "(10 points)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Part B directions missing %q", want)
		}
	}
	if strings.Contains(rendered, "答案：A") || strings.Contains(rendered, "答案：B") {
		t.Fatal("internal reference answers leaked into the printable exam")
	}
}

func TestEnglish2ClozeUsesCompactNumberedOptionRows(t *testing.T) {
	shared := "A short passage with [1] and [2]."
	paper := models.ExamPaper{
		Subject: "英语二",
		Questions: []models.GeneratedQuestion{
			{ID: "q1", Type: models.TypeChoice, GroupID: "english2-cloze", SharedMaterial: shared, Options: []string{"first", "second", "third", "fourth"}, Points: []string{"english2", "完形填空", "题位1"}, Stem: "Blank [1]"},
			{ID: "q2", Type: models.TypeChoice, GroupID: "english2-cloze", SharedMaterial: shared, Options: []string{"one", "two", "three", "four"}, Points: []string{"english2", "完形填空", "题位2"}, Stem: "Blank [2]"},
		},
	}
	rendered := New().renderExam(paper)
	if strings.Contains(rendered, "Blank [") {
		t.Fatalf("cloze stem leaked into exam output: %q", rendered)
	}
	if strings.Count(rendered, "\\begin{tabularx}") != 2 {
		t.Fatalf("got %d cloze rows, want 2: %q", strings.Count(rendered, "\\begin{tabularx}"), rendered)
	}
	for _, want := range []string{"1． & A．first & B．second & C．third & D．fourth", "2． & A．one & B．two & C．three & D．four"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("cloze row missing %q in %q", want, rendered)
		}
	}
}
