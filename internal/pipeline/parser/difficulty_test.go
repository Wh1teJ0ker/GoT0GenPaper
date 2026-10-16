package parser

import (
	"reflect"
	"testing"

	"GoT0GenPaper/internal/models"
)

func relativeDifficultyFixture() []models.PastQuestion {
	questions := []models.PastQuestion{
		// Low-score, frequently tested basics.
		{ID: "q1", Subject: "408", Type: models.TypeChoice, Points: []string{"数据结构", "线性表"}, Score: 2, DifficultyConfidence: 0.3},
		{ID: "q2", Subject: "408", Type: models.TypeChoice, Points: []string{"数据结构", "线性表"}, Score: 2, DifficultyConfidence: 0.3},
		{ID: "q3", Subject: "408", Type: models.TypeChoice, Points: []string{"数据结构", "线性表"}, Score: 2, DifficultyConfidence: 0.3},
		{ID: "q4", Subject: "408", Type: models.TypeChoice, Points: []string{"操作系统", "进程管理"}, Score: 2, DifficultyConfidence: 0.3},
		// High-score, rarely tested composites.
		{ID: "q5", Subject: "408", Type: models.TypeMajor, Points: []string{"数据结构", "B+树", "分裂"}, Score: 13, DifficultyConfidence: 0.3},
		{ID: "q6", Subject: "408", Type: models.TypeMajor, Points: []string{"组成原理", "总线定时"}, Score: 13, DifficultyConfidence: 0.3},
		// Real annotation must survive.
		{ID: "q7", Subject: "408", Type: models.TypeChoice, Points: []string{"计算机网络", "TCP"}, Score: 2, Difficulty: 0.8, DifficultyConfidence: 0.9},
	}
	return questions
}

func TestAssignRelativeDifficultySpreadsBandsAndKeepsKeysStable(t *testing.T) {
	got := AssignRelativeDifficulty(relativeDifficultyFixture())
	byID := make(map[string]models.PastQuestion, len(got))
	for _, q := range got {
		byID[q.ID] = q
	}

	// Repeated occurrences of one knowledge-point key must share a value, so the
	// composer's deduplicated pool keeps one band per topic.
	for _, id := range []string{"q1", "q2", "q3"} {
		if byID[id].Difficulty != byID["q1"].Difficulty {
			t.Fatalf("%s difficulty %.3f differs from q1 %.3f", id, byID[id].Difficulty, byID["q1"].Difficulty)
		}
	}
	// High-score, rare composite topics must land harder than frequent basics.
	if models.BandFromDifficulty(byID["q1"].Difficulty) >= models.BandFromDifficulty(byID["q5"].Difficulty) {
		t.Errorf("frequent basic (%.2f) not easier than rare composite (%.2f)", byID["q1"].Difficulty, byID["q5"].Difficulty)
	}
	// A real annotation (higher confidence) is never overwritten.
	if byID["q7"].Difficulty != 0.8 || byID["q7"].DifficultyConfidence != 0.9 {
		t.Errorf("annotated question was overwritten: %.2f/%.2f", byID["q7"].Difficulty, byID["q7"].DifficultyConfidence)
	}
	// Estimates are marked with the estimate confidence.
	if byID["q1"].DifficultyConfidence != relativeDifficultyConfidence {
		t.Errorf("estimate confidence = %.2f, want %.2f", byID["q1"].DifficultyConfidence, relativeDifficultyConfidence)
	}
	// Every band must be reachable from a small corpus, otherwise composition
	// could never match a blueprint histogram.
	bands := map[models.DifficultyBand]int{}
	for _, q := range got {
		if q.ID == "q7" {
			continue
		}
		bands[models.BandFromDifficulty(q.Difficulty)]++
	}
	if bands[models.BandEasy] == 0 || bands[models.BandMedium] == 0 || bands[models.BandHard] == 0 {
		t.Errorf("relative estimate left a band empty: %v", bands)
	}

	// Deterministic: the same corpus must produce the same assignment.
	again := AssignRelativeDifficulty(relativeDifficultyFixture())
	if !reflect.DeepEqual(got, again) {
		t.Fatal("relative difficulty assignment is not deterministic")
	}
}

func TestAssignRelativeDifficultyKeepsMath2HardBiased(t *testing.T) {
	questions := make([]models.PastQuestion, 0, 10)
	for i := 0; i < 10; i++ {
		questions = append(questions, models.PastQuestion{
			ID: "m" + string(rune('a'+i)), Subject: "math2", Type: models.TypeChoice,
			Points: []string{"高等数学", "topic-" + string(rune('a'+i))}, Score: 5, DifficultyConfidence: 0.3,
		})
	}
	got := AssignRelativeDifficulty(questions)
	bands := map[models.DifficultyBand]int{}
	for _, q := range got {
		bands[models.BandFromDifficulty(q.Difficulty)]++
	}
	// Math II's design target is 10/45/45; the estimate must not collapse to the
	// balanced layout, or a Math II paper could never reach its own target.
	if bands[models.BandHard] < bands[models.BandEasy] {
		t.Errorf("math2 estimate is not hard-biased: %v", bands)
	}
}
