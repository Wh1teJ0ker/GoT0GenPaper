package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

// TestLocalSearchTunesDifficultyHistogram pins the B-layer difficulty
// behaviour: local search must rebalance the selection's band histogram
// toward the blueprint target by swapping same-type equal-score candidates —
// without ever duplicating a candidate.
func TestLocalSearchTunesDifficultyHistogram(t *testing.T) {
	o := New(nil, nil)
	mk := func(i int, band models.DifficultyBand) candidateSpec {
		return candidateSpec{
			Type:     models.TypeChoice,
			Points:   []string{fmt.Sprintf("pt_%02d", i)},
			DiffBand: band,
			Score:    2,
			Prior:    0.3,
		}
	}
	var pool []candidateSpec
	for i := 0; i < 10; i++ {
		pool = append(pool, mk(i, models.BandEasy))
	}
	for i := 10; i < 20; i++ {
		pool = append(pool, mk(i, models.BandHard))
	}

	// Start from an all-easy selection against a 50/50 target.
	selection := []candidateSpec{pool[0], pool[1], pool[2], pool[3]}
	bp := &Blueprint{
		DiffHist: map[models.DifficultyBand]float64{
			models.BandEasy: 0.5,
			models.BandHard: 0.5,
		},
	}
	before := o.totalPenalty(selection, bp)
	selection = o.localSearch(pool, selection, bp)
	after := o.totalPenalty(selection, bp)
	if after >= before {
		t.Fatalf("localSearch did not reduce histogram penalty: %v -> %v", before, after)
	}

	counts := make(map[models.DifficultyBand]int)
	keys := make(map[string]bool)
	for _, c := range selection {
		counts[c.DiffBand]++
		keys[candidateKey(c)] = true
	}
	if counts[models.BandEasy] != 2 || counts[models.BandHard] != 2 {
		t.Errorf("band counts after tuning = %v, want easy=2 hard=2", counts)
	}
	if len(keys) != len(selection) {
		t.Errorf("localSearch introduced duplicate candidates: %d unique keys for %d slots", len(keys), len(selection))
	}
}

// TestExactFillOptionsNoCandidateReuse guards the subset-sum DP against the
// classic in-place-knapsack reconstruction bug: one candidate reused several
// times to fill a quota. Every returned selection must consist of distinct
// candidates summing exactly to the quota.
func TestExactFillOptionsNoCandidateReuse(t *testing.T) {
	cands := make([]candidateSpec, 0, 40)
	for i := 0; i < 30; i++ {
		cands = append(cands, candidateSpec{
			Type:   models.TypeMajor,
			Points: []string{fmt.Sprintf("kp_%02d", i)},
			Score:  10,
			Prior:  0.3,
		})
	}
	cands = append(cands, candidateSpec{Type: models.TypeMajor, Points: []string{"kp_best"}, Score: 10, Prior: 0.9})

	opts := exactFillOptions(cands, 70)
	if len(opts) == 0 {
		t.Fatal("quota 70 (7×10) infeasible with 31 candidates")
	}
	for k, sel := range opts {
		if len(sel) != k {
			t.Errorf("count option %d has %d candidates", k, len(sel))
		}
		total := 0.0
		seen := make(map[string]bool)
		for _, c := range sel {
			total += c.Score
			key := candidateKey(c)
			if seen[key] {
				t.Errorf("selection for count %d reuses candidate %v", k, c.Points)
			}
			seen[key] = true
		}
		if total != 70 {
			t.Errorf("selection for count %d totals %.0f, want 70", k, total)
		}
	}
}

func TestFixedCountTopUpUsesRealDisciplinePoints(t *testing.T) {
	base := []candidateSpec{
		{Type: models.TypeChoice, Points: []string{"高等数学", "一元函数微分学", "导数的计算"}, Score: 4, Prior: 0.4},
		{Type: models.TypeChoice, Points: []string{"高等数学", "一元函数积分学", "定积分的计算"}, Score: 4, Prior: 0.3},
	}
	catalogue := append(append([]candidateSpec(nil), base...), candidateSpec{
		Type:   models.TypeFillBlank,
		Points: []string{"高等数学", "多元函数微分学", "偏导数的概念与计算"},
		Score:  5,
	})
	bp := &Blueprint{BloomQuota: map[models.CognitiveLevel]int{models.CogApply: 1}}
	got := ensureFixedCountCandidatesForDiscipline(base, models.TypeChoice, 10, 2, bp, "高等数学", catalogue)
	if len(got) != 4 {
		t.Fatalf("top-up returned %d candidates, want 4", len(got))
	}
	for _, c := range got[2:] {
		if strings.Contains(strings.Join(c.Points, "/"), "fixed") {
			t.Fatalf("synthetic candidate contains marker point: %#v", c.Points)
		}
		if disciplineForPoints(c.Points) != "高等数学" {
			t.Fatalf("synthetic candidate escaped discipline: %#v", c.Points)
		}
	}
	options := exactFillOptions(got, 10)
	if len(options[2]) != 2 {
		t.Fatalf("top-up cannot satisfy 2×5 quota: %#v", options)
	}
}

func TestComposeEnforcesDisciplineQuotasAndOrder(t *testing.T) {
	var candidates []models.PastQuestion
	add := func(questionType models.QuestionType, discipline, point string, score float64) {
		candidates = append(candidates, models.PastQuestion{
			ID:         fmt.Sprintf("%s-%s-%d", questionType, discipline, len(candidates)),
			Type:       questionType,
			Points:     []string{discipline, point},
			Score:      score,
			Difficulty: 0.5,
		})
	}
	for i := 0; i < 7; i++ {
		add(models.TypeChoice, "高等数学", fmt.Sprintf("高数选择%d", i), 5)
	}
	for i := 0; i < 3; i++ {
		add(models.TypeChoice, "线性代数", fmt.Sprintf("线代选择%d", i), 5)
	}
	for i := 0; i < 5; i++ {
		add(models.TypeFillBlank, "高等数学", fmt.Sprintf("高数填空%d", i), 5)
	}
	add(models.TypeFillBlank, "线性代数", "线代填空", 5)
	for i, score := range []float64{10, 12, 12, 12, 12} {
		discipline := "高等数学"
		point := fmt.Sprintf("高数解答%d", i)
		if i == 1 {
			discipline = "常微分方程"
			point = "常微分方程解答"
		}
		add(models.TypeMajor, discipline, point, score)
	}
	add(models.TypeMajor, "线性代数", "线代解答", 12)

	bp := &Blueprint{
		Subject:    "数学二",
		TotalScore: 150,
		TypeQuota: map[models.QuestionType]float64{
			models.TypeChoice: 50, models.TypeFillBlank: 30, models.TypeMajor: 70,
		},
		TypeCountQuota: map[models.QuestionType]int{
			models.TypeChoice: 10, models.TypeFillBlank: 6, models.TypeMajor: 6,
		},
		DisciplineQuota: map[string]map[models.QuestionType]float64{
			"高等数学": {models.TypeChoice: 35, models.TypeFillBlank: 25, models.TypeMajor: 58},
			"线性代数": {models.TypeChoice: 15, models.TypeFillBlank: 5, models.TypeMajor: 12},
		},
		DisciplineCountQuota: map[string]map[models.QuestionType]int{
			"高等数学": {models.TypeChoice: 7, models.TypeFillBlank: 5, models.TypeMajor: 5},
			"线性代数": {models.TypeChoice: 3, models.TypeFillBlank: 1, models.TypeMajor: 1},
		},
		DisciplineOrder: []string{"高等数学", "线性代数"},
		NumQuestions:    22,
	}

	spec, err := New(nil, nil).Compose(context.Background(), bp, candidates)
	if err != nil {
		t.Fatalf("Compose returned error: %v", err)
	}
	if spec.TotalScore != 150 || len(spec.Entries) != 22 {
		t.Fatalf("spec totals = %.1f points/%d questions, want 150/22", spec.TotalScore, len(spec.Entries))
	}

	counts := make(map[string]map[models.QuestionType]int)
	scores := make(map[string]map[models.QuestionType]float64)
	seenLinearByType := make(map[models.QuestionType]bool)
	for _, entry := range spec.Entries {
		discipline := disciplineForPoints(entry.Points)
		if counts[discipline] == nil {
			counts[discipline] = make(map[models.QuestionType]int)
			scores[discipline] = make(map[models.QuestionType]float64)
		}
		counts[discipline][entry.Type]++
		scores[discipline][entry.Type] += entry.Score
		if discipline == "线性代数" {
			seenLinearByType[entry.Type] = true
		} else if discipline != "高等数学" {
			t.Errorf("unexpected discipline %q in %s entry %s", discipline, entry.Type, entry.ID)
		}
		if discipline == "高等数学" && seenLinearByType[entry.Type] {
			t.Errorf("高等数学 entry %s follows a linear algebra entry in %s", entry.ID, entry.Type)
		}
	}
	for discipline, quotas := range bp.DisciplineQuota {
		for questionType, want := range quotas {
			if scores[discipline][questionType] != want {
				t.Errorf("%s %s score = %.1f, want %.1f", discipline, questionType, scores[discipline][questionType], want)
			}
			if counts[discipline][questionType] != bp.DisciplineCountQuota[discipline][questionType] {
				t.Errorf("%s %s count = %d, want %d", discipline, questionType, counts[discipline][questionType], bp.DisciplineCountQuota[discipline][questionType])
			}
		}
	}
}
