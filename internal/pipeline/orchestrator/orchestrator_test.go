package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/quality"
)

func TestAllocateDifficultyCountsUsesLargestRemainder(t *testing.T) {
	counts, err := AllocateDifficultyCounts(map[models.DifficultyBand]float64{
		models.BandEasy: 0.1, models.BandMedium: 0.45, models.BandHard: 0.45,
	}, 22)
	if err != nil {
		t.Fatal(err)
	}
	want := map[models.DifficultyBand]int{models.BandEasy: 2, models.BandMedium: 10, models.BandHard: 10}
	for band, expected := range want {
		if counts[band] != expected {
			t.Errorf("count[%d] = %d, want %d", band, counts[band], expected)
		}
	}

	counts, err = AllocateDifficultyCounts(map[models.DifficultyBand]float64{
		models.BandEasy: 0.5, models.BandMedium: 0.5, models.BandHard: 0,
	}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if counts[models.BandEasy] != 3 || counts[models.BandMedium] != 2 {
		t.Fatalf("deterministic tie allocation = %#v, want easy=3 medium=2", counts)
	}
}

func TestDifficultyTargetCountsPrefersExplicitCounts(t *testing.T) {
	bp := &Blueprint{
		DiffHist:              map[models.DifficultyBand]float64{models.BandEasy: 1, models.BandMedium: 0, models.BandHard: 0},
		DiffCountQuota:        map[models.DifficultyBand]int{models.BandEasy: 1, models.BandMedium: 2, models.BandHard: 2},
		DifficultyCountStrict: true,
	}
	counts := DifficultyTargetCounts(bp, 5)
	if counts[models.BandEasy] != 1 || counts[models.BandMedium] != 2 || counts[models.BandHard] != 2 {
		t.Fatalf("explicit target counts ignored: %#v", counts)
	}
}

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

func TestLocalSearchTunesToExplicitDifficultyCounts(t *testing.T) {
	o := New(nil, nil)
	makeCandidate := func(i int, band models.DifficultyBand) candidateSpec {
		return candidateSpec{
			Type: models.TypeChoice, Points: []string{fmt.Sprintf("count_pt_%02d", i)},
			DiffBand: band, Score: 2, Prior: 0.3,
		}
	}
	pool := []candidateSpec{
		makeCandidate(0, models.BandEasy), makeCandidate(1, models.BandEasy),
		makeCandidate(2, models.BandHard), makeCandidate(3, models.BandHard),
	}
	selection := []candidateSpec{pool[0], pool[1]}
	selection = o.localSearch(pool, selection, &Blueprint{
		DiffHist:       map[models.DifficultyBand]float64{models.BandEasy: 0.5, models.BandHard: 0.5},
		DiffCountQuota: map[models.DifficultyBand]int{models.BandEasy: 1, models.BandMedium: 0, models.BandHard: 1},
	})
	counts := map[models.DifficultyBand]int{}
	for _, candidate := range selection {
		counts[candidate.DiffBand]++
	}
	if counts[models.BandEasy] != 1 || counts[models.BandHard] != 1 {
		t.Fatalf("explicit count target not reached: %#v", counts)
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

func TestStrictDifficultyCandidatesKeepSyllabusBackedPoints(t *testing.T) {
	used := make(map[string]bool)
	points := nextSyntheticPointSet("高等数学", models.TypeChoice, used, 0)
	if len(points) == 0 {
		t.Fatal("nextSyntheticPointSet returned no candidate")
	}
	if issues := quality.SubjectContentIssues("数学二", points, ""); len(issues) > 0 {
		t.Fatalf("synthetic high-math points failed syllabus validation: %v; points=%v", issues, points)
	}
	if disciplineForPoints(points) != "高等数学" {
		t.Fatalf("synthetic candidate discipline = %q, want 高等数学: %v", disciplineForPoints(points), points)
	}

	catalogue := []candidateSpec{{
		Type:   models.TypeChoice,
		Points: []string{"高等数学", "一元函数微分学", "导数的计算"},
		Score:  5,
	}}
	generated := augmentCellCandidates(nil, catalogue, models.TypeChoice, "高等数学", 5, 1, &Blueprint{})
	if len(generated) == 0 {
		t.Fatal("augmentCellCandidates returned no candidate")
	}
	if strings.Contains(strings.Join(generated[0].Points, "/"), "难度目标候选") {
		t.Fatalf("real syllabus catalogue was skipped in favour of a synthetic marker: %v", generated[0].Points)
	}
	seen := make(map[string]bool)
	for _, candidate := range generated {
		key := candidateKey(candidate)
		if seen[key] {
			t.Fatalf("difficulty augmentation duplicated a knowledge-point set: %v", candidate.Points)
		}
		seen[key] = true
	}
}

func TestDifficultySelectionTieBreakIsDeterministic(t *testing.T) {
	candidates := []models.PastQuestion{
		{Type: models.TypeChoice, Points: []string{"高等数学", "极限", "点A"}, Score: 5, Difficulty: 0.2},
		{Type: models.TypeChoice, Points: []string{"高等数学", "导数", "点B"}, Score: 5, Difficulty: 0.2},
		{Type: models.TypeChoice, Points: []string{"高等数学", "积分", "点C"}, Score: 5, Difficulty: 0.5},
		{Type: models.TypeChoice, Points: []string{"高等数学", "极限", "点D"}, Score: 5, Difficulty: 0.5},
	}
	bp := &Blueprint{
		Subject: "数学二", TotalScore: 10,
		TypeQuota:             map[models.QuestionType]float64{models.TypeChoice: 10},
		TypeCountQuota:        map[models.QuestionType]int{models.TypeChoice: 2},
		DiffHist:              map[models.DifficultyBand]float64{models.BandEasy: 0.5, models.BandMedium: 0.5, models.BandHard: 0},
		DiffCountQuota:        map[models.DifficultyBand]int{models.BandEasy: 1, models.BandMedium: 1, models.BandHard: 0},
		DifficultyCountStrict: true, NumQuestions: 2,
	}
	first, err := New(nil, nil).Compose(context.Background(), bp, candidates)
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 5; run++ {
		got, err := New(nil, nil).Compose(context.Background(), bp, candidates)
		if err != nil {
			t.Fatal(err)
		}
		if selectionKeyFromSpec(got) != selectionKeyFromSpec(first) {
			t.Fatalf("run %d selected a different spec: %s vs %s", run, selectionKeyFromSpec(got), selectionKeyFromSpec(first))
		}
	}
}

func selectionKeyFromSpec(spec *models.SpecTable) string {
	keys := make([]string, 0, len(spec.Entries))
	for _, entry := range spec.Entries {
		keys = append(keys, fmt.Sprintf("%s|%s|%.1f|%d", entry.Type, strings.Join(entry.Points, ","), entry.Score, entry.DiffBand))
	}
	return strings.Join(keys, "\x00")
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
