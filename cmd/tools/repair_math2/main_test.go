package main

import (
	"path/filepath"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

func TestCurrentMath2SpecMatchesDisciplineLayout(t *testing.T) {
	var spec models.SpecTable
	path := filepath.Join("..", "..", "..", "data", "spec_table.json")
	if err := readJSON(path, &spec); err != nil {
		t.Fatalf("read current spec: %v", err)
	}
	cleanMath2Spec(&spec)
	if err := sortAndValidateSpecEntries(&spec); err != nil {
		t.Fatalf("sort current spec: %v", err)
	}
	if spec.TotalScore != 150 || len(spec.Entries) != 22 {
		t.Fatalf("current spec totals = %.1f points/%d entries, want 150/22", spec.TotalScore, len(spec.Entries))
	}

	wantScores := map[string]map[models.QuestionType]float64{
		"高等数学": {models.TypeChoice: 35, models.TypeFillBlank: 25, models.TypeMajor: 58},
		"线性代数": {models.TypeChoice: 15, models.TypeFillBlank: 5, models.TypeMajor: 12},
	}
	wantCounts := map[string]map[models.QuestionType]int{
		"高等数学": {models.TypeChoice: 7, models.TypeFillBlank: 5, models.TypeMajor: 5},
		"线性代数": {models.TypeChoice: 3, models.TypeFillBlank: 1, models.TypeMajor: 1},
	}
	gotScores := make(map[string]map[models.QuestionType]float64)
	gotCounts := make(map[string]map[models.QuestionType]int)
	lastDiscipline := make(map[models.QuestionType]int)
	seenType := make(map[models.QuestionType]bool)
	for i, entry := range spec.Entries {
		if entry.Index != i+1 {
			t.Fatalf("entry %s has index %d at position %d", entry.ID, entry.Index, i+1)
		}
		discipline := specDiscipline(entry.Points)
		if gotScores[discipline] == nil {
			gotScores[discipline] = make(map[models.QuestionType]float64)
			gotCounts[discipline] = make(map[models.QuestionType]int)
		}
		gotScores[discipline][entry.Type] += entry.Score
		gotCounts[discipline][entry.Type]++
		order := map[string]int{"高等数学": 0, "线性代数": 1}[discipline]
		if seenType[entry.Type] && order < lastDiscipline[entry.Type] {
			t.Errorf("%s discipline ordering regressed at entry %s", entry.Type, entry.ID)
		}
		seenType[entry.Type] = true
		lastDiscipline[entry.Type] = order
	}
	for discipline, typeScores := range wantScores {
		for questionType, want := range typeScores {
			if gotScores[discipline][questionType] != want {
				t.Errorf("%s %s score = %.1f, want %.1f", discipline, questionType, gotScores[discipline][questionType], want)
			}
			if gotCounts[discipline][questionType] != wantCounts[discipline][questionType] {
				t.Errorf("%s %s count = %d, want %d", discipline, questionType, gotCounts[discipline][questionType], wantCounts[discipline][questionType])
			}
		}
	}

	if spec.Entries[14].Points[0] != "高等数学" {
		t.Errorf("question 15 root discipline = %q, want 高等数学", spec.Entries[14].Points[0])
	}
	if spec.Entries[15].Points[0] != "线性代数" || spec.Entries[20].Points[0] != "高等数学" || spec.Entries[21].Points[0] != "线性代数" {
		t.Error("questions 16, 21, and 22 are not assigned to their intended disciplines")
	}
	if spec.Entries[16].Score != 10 || spec.Entries[17].Score != 12 {
		t.Errorf("first two major-question scores = %.0f and %.0f, want 10 and 12", spec.Entries[16].Score, spec.Entries[17].Score)
	}

	contents := cleanMath2Contents()
	wantChoiceStems := map[string]string{
		"spec_001": "f'(0)=2",
		"spec_002": "f(\\cos x)",
		"spec_003": "F'(1)",
		"spec_004": "全微分",
		"spec_005": "1-\\cos x",
		"spec_006": "e^{x^2}",
		"spec_007": "x\\ln x",
		"spec_008": "A^3-3A^2",
		"spec_009": "\\alpha_1",
		"spec_010": "A^2=A",
	}
	for id, marker := range wantChoiceStems {
		if !strings.Contains(contents[id].Stem, marker) {
			t.Errorf("%s stem = %q, want marker %q", id, contents[id].Stem, marker)
		}
	}
	if !strings.Contains(contents["spec_016"].Stem, "r(A)=2") || !strings.Contains(contents["spec_016"].Answer, "a=1") {
		t.Error("linear algebra fill-in question or answer is missing")
	}
	if !strings.Contains(contents["spec_021"].Stem, "\\iint") || !strings.Contains(contents["spec_021"].Answer, "3-\\ln") {
		t.Error("calculus major question or polar-coordinate solution is missing")
	}
}

func TestSortAndValidateSpecEntriesRejectsGaps(t *testing.T) {
	spec := models.SpecTable{Entries: []models.SpecEntry{
		{ID: "spec_001", Index: 1},
		{ID: "spec_003", Index: 3},
	}}
	if err := sortAndValidateSpecEntries(&spec); err == nil {
		t.Fatal("sortAndValidateSpecEntries accepted non-contiguous indices")
	}
}

func specDiscipline(points []string) string {
	return models.DisciplineFromPoints(points)
}
