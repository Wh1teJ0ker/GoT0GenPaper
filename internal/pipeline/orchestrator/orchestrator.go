// Package orchestrator implements Stage [3]: CP-SAT 编排层.
//
// Models exam composition as a constraint satisfaction + optimisation problem.
// Decision variables: each题位 (知识点子图, 难度band, 题型, 认知层, 分值).
//
// Three-tier constraint executability:
//
//	A. spec-level hard constraints (总分/配额/覆盖下限/Bloom配额) — enforced
//	   via exact-fill subset-sum DP per type quota
//	B. spec-level soft targets (蓝图prior/难度直方图/usage cap/题位数) —
//	   count selection + local-search penalty
//	C. artifact-level (生成后才能测) — verify+re-spec loop, NOT in orchestrator
//
// MVP uses a pure-Go solver: per-type 0/1-knapsack DP guarantees each type
// quota is hit exactly when the candidate pool allows it (mixed question
// scores across exam eras make naive greedy undershoot), then a cross-type
// count selection approaches NumQuestions, then local search tunes the
// difficulty histogram with same-type equal-score swaps.
package orchestrator

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"GoT0GenPaper/internal/models"
)

// Blueprint defines the target distribution for exam composition.
//
// NOTE: fields intentionally carry no json tags to preserve the existing
// persisted blueprint format, which uses Go field names (PascalCase).
type Blueprint struct {
	Subject               string
	TotalScore            float64                                    // 目标总分(如150)
	TypeQuota             map[models.QuestionType]float64            // 题型×分值配额
	TypeCountQuota        map[models.QuestionType]int                // 题型×题数硬约束
	DisciplineQuota       map[string]map[models.QuestionType]float64 // 学科×题型分值硬约束
	DisciplineCountQuota  map[string]map[models.QuestionType]int     // 学科×题型题数硬约束
	DisciplineOrder       []string                                   // 同题型内的学科排序
	CoverageFloor         map[string]float64                         // 章节/知识点→最低覆盖分(大纲硬约束,A层)
	BloomQuota            map[models.CognitiveLevel]int              // Bloom认知层→最低题数(A层)
	DiffHist              map[models.DifficultyBand]float64          // 难度直方图目标比例(B层)
	DiffCountQuota        map[models.DifficultyBand]int              // 难度目标题数；非空时优先于比例
	DifficultyCountStrict bool                                       // 用户显式配置时，题数目标为硬约束
	UsageCap              map[string]int                             // 每知识点全卷≤N次(B层软约束)
	NumQuestions          int                                        // 目标题位数 (0 = 由配额推断；无 TypeCountQuota 时为软目标)
}

// Orchestrator wraps the constraint solver with GNN priors.
type Orchestrator struct {
	GNNWeights      map[string]map[string]float64
	GNNCooccurrence map[string]map[string]float64
}

// New creates an Orchestrator with GNN priors (nil for MVP statistical).
func New(weights, coocc map[string]map[string]float64) *Orchestrator {
	return &Orchestrator{GNNWeights: weights, GNNCooccurrence: coocc}
}

// candidateSpec is a potential题位 assignment generated from the candidate pool.
type candidateSpec struct {
	Type      models.QuestionType
	Points    []string
	Cognitive models.CognitiveLevel
	DiffBand  models.DifficultyBand
	Score     float64
	Template  string
	Prior     float64 // GNN weight prior (higher = more likely to appear)
}

// Compose runs the constraint solver to produce the spec table (生成前双向细目表).
//
// Algorithm:
//  1. Build candidate pool from past questions, deduplicated by
//     (type, knowledge points) — the highest-prior variant wins — so one
//     paper can never select the same knowledge-point set twice (intra-paper
//     Jaccard 1.0 duplicates).
//  2. Exact-fill score and count quotas by question type, optionally split
//     further by discipline. Infeasible fixed-count quotas get low-priority
//     synthetic candidates at a feasible score pattern.
//  3. If discipline quotas are configured, keep those hard quotas intact
//     during local search and sort each question type by DisciplineOrder.
//  4. Local-search improve: same-type equal-score swaps to reduce the
//     B-layer penalty (difficulty histogram / usage caps).
func (o *Orchestrator) Compose(ctx context.Context, bp *Blueprint, candidates []models.PastQuestion) (*models.SpecTable, error) {
	if bp == nil {
		return nil, fmt.Errorf("blueprint is nil")
	}
	if bp.TotalScore <= 0 {
		return nil, fmt.Errorf("blueprint totalScore must be positive")
	}
	if len(bp.TypeQuota) == 0 {
		return nil, fmt.Errorf("blueprint has no type quotas")
	}

	// Build the deduplicated candidate pool.
	pool := o.buildCandidatePool(candidates)
	if len(pool) == 0 {
		return nil, fmt.Errorf("no candidates available for composition")
	}

	// Deterministic type order (map iteration would be random).
	types := make([]models.QuestionType, 0, len(bp.TypeQuota))
	for t := range bp.TypeQuota {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })

	compositionPool := pool
	if bp.DifficultyCountStrict && len(bp.DiffCountQuota) > 0 {
		compositionPool = augmentDifficultyPool(pool, bp)
	}
	var selection []candidateSpec
	if len(bp.DisciplineQuota) > 0 {
		var err error
		if bp.DifficultyCountStrict && len(bp.DiffCountQuota) > 0 {
			selection, err = selectDisciplineQuotasWithDifficulty(compositionPool, bp, types)
		} else {
			selection, err = selectDisciplineQuotas(compositionPool, bp, types)
		}
		if err != nil {
			return nil, err
		}
	} else if bp.DifficultyCountStrict && len(bp.DiffCountQuota) > 0 {
		var err error
		selection, err = selectTypeQuotasWithDifficulty(compositionPool, bp, types)
		if err != nil {
			return nil, err
		}
	} else {
		// Per-type exact-fill options: question count → a selection whose scores
		// sum exactly to that type's quota.
		options := make(map[models.QuestionType]map[int][]candidateSpec, len(types))
		for _, t := range types {
			cands := poolByType(pool, t)
			if want, fixed := bp.TypeCountQuota[t]; fixed {
				cands = ensureFixedCountCandidates(cands, t, bp.TypeQuota[t], want, bp)
			}
			opts := exactFillOptions(cands, bp.TypeQuota[t])
			if len(opts) == 0 {
				cands = augmentType(cands, t, bp.TypeQuota[t], bp)
				opts = exactFillOptions(cands, bp.TypeQuota[t])
			}
			if len(opts) == 0 {
				// Quota infeasible even after augmentation — best effort with a
				// documented shortfall (surfaced downstream via the blueprint
				// score check).
				sel := greedyType(cands, bp.TypeQuota[t])
				if len(sel) == 0 {
					return nil, fmt.Errorf("no feasible candidates for type %s", t)
				}
				opts = map[int][]candidateSpec{len(sel): sel}
			}
			options[t] = opts
		}

		if len(bp.TypeCountQuota) > 0 {
			var err error
			selection, err = selectFixedCounts(options, types, bp.TypeCountQuota, bp.NumQuestions)
			if err != nil {
				return nil, err
			}
		} else {
			selection = combineCounts(options, types, bp.NumQuestions)
			if len(selection) == 0 {
				return nil, fmt.Errorf("failed to find a feasible spec table")
			}
		}
	}

	// Local-search improvement (same-type equal-score swaps). When an explicit
	// difficulty target is active, add low-priority syllabus-backed variants to
	// the search catalogue so a missing historical band does not make a target
	// unreachable merely because the corpus lacks that exact annotation.
	selection = o.localSearch(compositionPool, selection, bp)

	// Deterministic spec-entry order: blueprint type order, then prior.
	typeIndex := make(map[models.QuestionType]int, len(types))
	for i, t := range types {
		typeIndex[t] = i
	}
	sort.SliceStable(selection, func(i, j int) bool {
		ti, tj := typeIndex[selection[i].Type], typeIndex[selection[j].Type]
		if ti != tj {
			return ti < tj
		}
		if len(bp.DisciplineOrder) > 0 {
			di := disciplineOrderIndex(disciplineForPoints(selection[i].Points), bp.DisciplineOrder)
			dj := disciplineOrderIndex(disciplineForPoints(selection[j].Points), bp.DisciplineOrder)
			if di != dj {
				return di < dj
			}
		}
		if selection[i].Prior != selection[j].Prior {
			return selection[i].Prior > selection[j].Prior
		}
		return strings.Join(selection[i].Points, ",") < strings.Join(selection[j].Points, ",")
	})

	entries := make([]models.SpecEntry, 0, len(selection))
	for i, c := range selection {
		entries = append(entries, models.SpecEntry{
			ID:          fmt.Sprintf("spec_%03d", i+1),
			Index:       i + 1,
			Type:        c.Type,
			Points:      c.Points,
			Cognitive:   c.Cognitive,
			DiffBand:    c.DiffBand,
			Score:       c.Score,
			TemplateKey: c.Template,
		})
	}

	return &models.SpecTable{
		Entries:                entries,
		TotalScore:             sumScores(selection),
		Subject:                bp.Subject,
		DifficultyTarget:       DifficultyTargetCounts(bp, len(entries)),
		DifficultyTargetStrict: bp.DifficultyCountStrict,
	}, nil
}

func selectDisciplineQuotas(pool []candidateSpec, bp *Blueprint, types []models.QuestionType) ([]candidateSpec, error) {
	if len(bp.DisciplineCountQuota) != len(bp.DisciplineQuota) {
		return nil, fmt.Errorf("discipline score and count quotas must cover the same disciplines")
	}
	disciplines := make([]string, 0, len(bp.DisciplineQuota))
	for discipline := range bp.DisciplineQuota {
		if _, ok := bp.DisciplineCountQuota[discipline]; !ok {
			return nil, fmt.Errorf("missing count quota for discipline %q", discipline)
		}
		disciplines = append(disciplines, discipline)
	}
	sort.Slice(disciplines, func(i, j int) bool {
		di := disciplineOrderIndex(disciplines[i], bp.DisciplineOrder)
		dj := disciplineOrderIndex(disciplines[j], bp.DisciplineOrder)
		if di != dj {
			return di < dj
		}
		return disciplines[i] < disciplines[j]
	})

	totalCount := 0
	for _, t := range types {
		var scoreSum float64
		countSum := 0
		for _, discipline := range disciplines {
			scoreQuota, ok := bp.DisciplineQuota[discipline][t]
			if !ok {
				return nil, fmt.Errorf("missing %s score quota for discipline %q", t, discipline)
			}
			countQuota, ok := bp.DisciplineCountQuota[discipline][t]
			if !ok {
				return nil, fmt.Errorf("missing %s count quota for discipline %q", t, discipline)
			}
			if scoreQuota < 0 || countQuota < 0 || (scoreQuota == 0) != (countQuota == 0) {
				return nil, fmt.Errorf("invalid %s quota for discipline %q: %.1f points, %d questions", t, discipline, scoreQuota, countQuota)
			}
			scoreSum += scoreQuota
			countSum += countQuota
		}
		if math.Abs(scoreSum-bp.TypeQuota[t]) > 0.01 {
			return nil, fmt.Errorf("discipline quotas for %s total %.1f points, type quota is %.1f", t, scoreSum, bp.TypeQuota[t])
		}
		want, ok := bp.TypeCountQuota[t]
		if !ok || countSum != want {
			return nil, fmt.Errorf("discipline quotas for %s total %d questions, type count quota is %d", t, countSum, want)
		}
		totalCount += countSum
	}
	if bp.NumQuestions > 0 && totalCount != bp.NumQuestions {
		return nil, fmt.Errorf("discipline count quotas total %d questions, want %d", totalCount, bp.NumQuestions)
	}

	selection := make([]candidateSpec, 0, totalCount)
	for _, t := range types {
		for _, discipline := range disciplines {
			scoreQuota := bp.DisciplineQuota[discipline][t]
			countQuota := bp.DisciplineCountQuota[discipline][t]
			if countQuota == 0 {
				continue
			}
			cands := make([]candidateSpec, 0)
			for _, c := range pool {
				if c.Type == t && disciplineForPoints(c.Points) == discipline {
					cands = append(cands, c)
				}
			}
			// If the historical score pattern cannot satisfy the fixed count,
			// synthetic candidates must still point at real syllabus nodes. Use
			// candidates from every question type in this discipline as the
			// source catalogue; only the requested type is selected below.
			cands = ensureFixedCountCandidatesForDiscipline(cands, t, scoreQuota, countQuota, bp, discipline, pool)
			option := exactFillOptions(cands, scoreQuota)[countQuota]
			if len(option) != countQuota {
				return nil, fmt.Errorf("cannot satisfy %s/%s quota: %.1f points in %d questions", discipline, t, scoreQuota, countQuota)
			}
			selection = append(selection, option...)
		}
	}
	return selection, nil
}

// selectDisciplineQuotasWithDifficulty keeps the official type/discipline
// score and count quotas hard, then solves the remaining difficulty-count
// allocation across those cells. Each cell contributes exact-fill options
// annotated with its easy/medium/hard counts; a small DP combines them.
func selectDisciplineQuotasWithDifficulty(pool []candidateSpec, bp *Blueprint, types []models.QuestionType) ([]candidateSpec, error) {
	disciplines := make([]string, 0, len(bp.DisciplineQuota))
	for discipline := range bp.DisciplineQuota {
		disciplines = append(disciplines, discipline)
	}
	sort.Slice(disciplines, func(i, j int) bool {
		di, dj := disciplineOrderIndex(disciplines[i], bp.DisciplineOrder), disciplineOrderIndex(disciplines[j], bp.DisciplineOrder)
		if di != dj {
			return di < dj
		}
		return disciplines[i] < disciplines[j]
	})
	targets := DifficultyTargetCounts(bp, bp.NumQuestions)
	if len(targets) == 0 {
		return nil, fmt.Errorf("difficulty target counts unavailable")
	}
	cells := make([]difficultyCell, 0)
	for _, t := range types {
		for _, discipline := range disciplines {
			score := bp.DisciplineQuota[discipline][t]
			count := bp.DisciplineCountQuota[discipline][t]
			if count == 0 {
				continue
			}
			cands := candidatesForCell(pool, t, discipline)
			// Always add a bounded set of low-priority variants. A cell can have
			// some exact-fill options while still lacking the particular band
			// vector needed by the whole-paper target.
			cands = augmentCellCandidates(cands, pool, t, discipline, score, count, bp)
			options := exactFillDifficultyOptions(cands, score, count)
			if len(options) == 0 {
				return nil, fmt.Errorf("cannot satisfy %s/%s quota with requested difficulty bands", discipline, t)
			}
			cells = append(cells, difficultyCell{options: options, typeName: t, discipline: discipline})
		}
	}
	selection, ok := combineDifficultyCells(cells, targets)
	if !ok {
		return nil, fmt.Errorf("difficulty target counts cannot be satisfied by the available type/discipline candidates")
	}
	return selection, nil
}

func selectTypeQuotasWithDifficulty(pool []candidateSpec, bp *Blueprint, types []models.QuestionType) ([]candidateSpec, error) {
	targets := DifficultyTargetCounts(bp, bp.NumQuestions)
	cells := make([]difficultyCell, 0, len(types))
	for _, t := range types {
		score, count := bp.TypeQuota[t], bp.TypeCountQuota[t]
		cands := poolByType(pool, t)
		cands = augmentCellCandidates(cands, pool, t, "", score, count, bp)
		options := exactFillDifficultyOptions(cands, score, count)
		if len(options) == 0 {
			return nil, fmt.Errorf("cannot satisfy %s quota with requested difficulty bands", t)
		}
		cells = append(cells, difficultyCell{options: options, typeName: t})
	}
	selection, ok := combineDifficultyCells(cells, targets)
	if !ok {
		return nil, fmt.Errorf("difficulty target counts cannot be satisfied by available candidates")
	}
	return selection, nil
}

func augmentCellCandidates(cands, catalogue []candidateSpec, t models.QuestionType, discipline string, score float64, count int, bp *Blueprint) []candidateSpec {
	out := append([]candidateSpec(nil), cands...)
	used := make(map[string]bool, len(out))
	for _, c := range out {
		used[candidateKey(c)] = true
	}
	pointSets := appendUniquePointSets(pointSetCatalogue(catalogue, discipline), fallbackPointSets(discipline))
	scores := make([]float64, 0)
	seenScores := make(map[float64]bool)
	for _, candidate := range cands {
		if candidate.Score > 0 && !seenScores[candidate.Score] {
			seenScores[candidate.Score] = true
			scores = append(scores, candidate.Score)
		}
	}
	average := score / float64(count)
	for _, q2 := range []int{int(math.Floor(average * 2)), int(math.Ceil(average * 2))} {
		candidateScore := float64(q2) / 2
		if q2 > 0 && !seenScores[candidateScore] {
			seenScores[candidateScore] = true
			scores = append(scores, candidateScore)
		}
	}
	sort.Float64s(scores)
	if len(scores) == 0 {
		scores = []float64{typicalScore(t)}
	}
	for _, band := range difficultyBands {
		for scoreIndex, candidateScore := range scores {
			// Prefer a real syllabus path. Synthetic paths are only a last
			// resort after the catalogue is exhausted.
			points := nextPointSet(pointSets, used, t, len(out)+scoreIndex+int(band))
			if len(points) == 0 {
				points = nextSyntheticPointSet(discipline, t, used, len(out)+scoreIndex+int(band))
			}
			if len(points) == 0 {
				continue
			}
			out = append(out, candidateSpec{Type: t, Points: points, Cognitive: pickCognitive(bp), DiffBand: band, Score: candidateScore, Template: fmt.Sprintf("%s_%s", t, firstOr(points, discipline)), Prior: 0.005})
			used[candidateKey(out[len(out)-1])] = true
		}
	}
	return out
}

type difficultyCell struct {
	options    []difficultyOption
	typeName   models.QuestionType
	discipline string
}

type difficultyOption struct {
	selection []candidateSpec
	counts    map[models.DifficultyBand]int
	prior     float64
}

func candidatesForCell(pool []candidateSpec, t models.QuestionType, discipline string) []candidateSpec {
	out := make([]candidateSpec, 0)
	for _, candidate := range pool {
		if candidate.Type == t && disciplineForPoints(candidate.Points) == discipline {
			out = append(out, candidate)
		}
	}
	return out
}

// exactFillDifficultyOptions enumerates exact score/count selections and
// retains the highest-prior selection for each band-count vector.
func exactFillDifficultyOptions(cands []candidateSpec, quota float64, count int) []difficultyOption {
	if count <= 0 {
		return nil
	}
	items := append([]candidateSpec(nil), cands...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Prior > items[j].Prior })
	if len(items) > 512 {
		items = items[:512]
	}
	Q := int(math.Round(quota * 2))
	type state struct {
		score, count       int
		easy, medium, hard int
		prior              float64
		selection          []candidateSpec
	}
	states := map[[5]int]state{{0, 0, 0, 0, 0}: {score: 0, count: 0, selection: []candidateSpec{}}}
	for _, candidate := range items {
		q2 := int(math.Round(candidate.Score * 2))
		if q2 <= 0 || q2 > Q {
			continue
		}
		next := make(map[[5]int]state, len(states)*2)
		put := func(key [5]int, value state) {
			if previous, exists := next[key]; !exists || betterState(value.prior, value.selection, previous.prior, previous.selection) {
				next[key] = value
			}
		}
		for key, current := range states {
			put(key, current)
			if current.count >= count || current.score+q2 > Q {
				continue
			}
			n := current
			n.score += q2
			n.count++
			n.prior += candidate.Prior
			switch candidate.DiffBand {
			case models.BandEasy:
				n.easy++
			case models.BandMedium:
				n.medium++
			case models.BandHard:
				n.hard++
			}
			n.selection = append(append([]candidateSpec(nil), current.selection...), candidate)
			put([5]int{n.score, n.count, n.easy, n.medium, n.hard}, n)
		}
		states = next
	}
	options := make([]difficultyOption, 0)
	for key, state := range states {
		if key[0] != Q || key[1] != count {
			continue
		}
		options = append(options, difficultyOption{selection: state.selection, counts: map[models.DifficultyBand]int{models.BandEasy: key[2], models.BandMedium: key[3], models.BandHard: key[4]}, prior: state.prior})
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].prior != options[j].prior {
			return options[i].prior > options[j].prior
		}
		return selectionKey(options[i].selection) < selectionKey(options[j].selection)
	})
	return options
}

func combineDifficultyCells(cells []difficultyCell, targets map[models.DifficultyBand]int) ([]candidateSpec, bool) {
	type key struct{ easy, medium, hard int }
	type state struct {
		prior     float64
		selection []candidateSpec
	}
	states := map[key]state{{}: {}}
	for _, cell := range cells {
		next := make(map[key]state)
		for currentKey, current := range states {
			for _, option := range cell.options {
				candidateKey := key{currentKey.easy + option.counts[models.BandEasy], currentKey.medium + option.counts[models.BandMedium], currentKey.hard + option.counts[models.BandHard]}
				if candidateKey.easy > targets[models.BandEasy] || candidateKey.medium > targets[models.BandMedium] || candidateKey.hard > targets[models.BandHard] {
					continue
				}
				nextState := state{prior: current.prior + option.prior, selection: append(append([]candidateSpec(nil), current.selection...), option.selection...)}
				if previous, exists := next[candidateKey]; !exists || betterState(nextState.prior, nextState.selection, previous.prior, previous.selection) {
					next[candidateKey] = nextState
				}
			}
		}
		states = next
	}
	want := key{targets[models.BandEasy], targets[models.BandMedium], targets[models.BandHard]}
	result, ok := states[want]
	return result.selection, ok
}

func disciplineForPoints(points []string) string {
	return models.DisciplineFromPoints(points)
}

func disciplineOrderIndex(discipline string, order []string) int {
	for i, item := range order {
		if item == discipline {
			return i
		}
	}
	return len(order)
}

// ensureFixedCountCandidates adds low-priority synthetic candidates only when
// deduplicating historical knowledge-point sets removed enough score variants
// to make an official count impossible. The score pattern is solved first,
// so fixed count and fixed type score remain hard constraints together.
func ensureFixedCountCandidates(cands []candidateSpec, t models.QuestionType, quota float64, want int, bp *Blueprint) []candidateSpec {
	return ensureFixedCountCandidatesForDiscipline(cands, t, quota, want, bp, "", cands)
}

func ensureFixedCountCandidatesForDiscipline(cands []candidateSpec, t models.QuestionType, quota float64, want int, bp *Blueprint, discipline string, catalogue []candidateSpec) []candidateSpec {
	if want <= 0 || len(exactFillOptions(cands, quota)[want]) > 0 {
		return cands
	}

	scores := make([]float64, 0)
	seen := make(map[int]bool)
	for _, c := range cands {
		q2 := int(math.Round(c.Score * 2))
		if q2 > 0 && !seen[q2] {
			seen[q2] = true
			scores = append(scores, float64(q2)/2)
		}
	}
	if len(scores) == 0 {
		scores = append(scores, typicalScore(t))
	}
	// A nearby half-point pair gives the fallback solver a valid pattern for
	// quotas whose average score is not itself a half-point, e.g. 70/6.
	average := quota / float64(want)
	for _, q2 := range []int{int(math.Floor(average * 2)), int(math.Ceil(average * 2))} {
		if q2 > 0 && !seen[q2] {
			seen[q2] = true
			scores = append(scores, float64(q2)/2)
		}
	}

	pattern := fixedScorePattern(scores, want, quota)
	if len(pattern) == 0 {
		return cands
	}
	out := append([]candidateSpec(nil), cands...)
	usedKeys := make(map[string]bool, len(out))
	for _, c := range out {
		usedKeys[candidateKey(c)] = true
	}
	pointSets := pointSetCatalogue(catalogue, discipline)
	pointSets = appendUniquePointSets(pointSets, fallbackPointSets(discipline))
	bands := []models.DifficultyBand{models.BandEasy, models.BandMedium, models.BandHard}
	for i, score := range pattern {
		var points []string
		if discipline == "" {
			// Non-subject-specific blueprints have no syllabus namespace to
			// validate. Preserve the generic fallback used by those profiles.
			points = []string{fmt.Sprintf("%s_auto_%d", t, i)}
		} else {
			points = nextPointSet(pointSets, usedKeys, t, i)
		}
		if len(points) == 0 {
			return cands
		}
		template := fmt.Sprintf("%s_%s", t, firstOr(points, discipline))
		out = append(out, candidateSpec{
			Type:      t,
			Points:    points,
			Cognitive: pickCognitive(bp),
			DiffBand:  bands[i%len(bands)],
			Score:     score,
			Template:  template,
			Prior:     0.01,
		})
	}
	if bp.DifficultyCountStrict && len(bp.DiffCountQuota) > 0 {
		out = augmentDifficultyCandidates(out, t, scores, bp, discipline, catalogue)
	}
	return out
}

// augmentDifficultyCandidates adds low-priority, syllabus-backed candidates
// for missing band/score combinations. Historical corpora often contain only
// medium 5-point choices even though the caller requests hard choices; without
// these variants, a fixed score quota makes a valid difficulty target
// impossible for purely arithmetic reasons.
func augmentDifficultyCandidates(cands []candidateSpec, t models.QuestionType, scores []float64, bp *Blueprint, discipline string, catalogue []candidateSpec) []candidateSpec {
	pointSets := pointSetCatalogue(catalogue, discipline)
	pointSets = appendUniquePointSets(pointSets, fallbackPointSets(discipline))
	usedKeys := make(map[string]bool, len(cands))
	for _, c := range cands {
		usedKeys[candidateKey(c)] = true
	}
	out := append([]candidateSpec(nil), cands...)
	for _, score := range scores {
		for _, band := range difficultyBands {
			needed := 1
			if discipline != "" {
				needed = bp.DisciplineCountQuota[discipline][t]
			} else if bp.TypeCountQuota[t] > needed {
				needed = bp.TypeCountQuota[t]
			}
			for count := countCandidates(out, t, score, band); count < needed; count++ {
				points := nextPointSet(pointSets, usedKeys, t, len(out)+int(band)+count)
				if len(points) == 0 {
					points = nextSyntheticPointSet(discipline, t, usedKeys, len(out)+int(band)+count)
				}
				if len(points) == 0 {
					break
				}
				out = append(out, candidateSpec{
					Type: t, Points: points, Cognitive: pickCognitive(bp),
					DiffBand: band, Score: score,
					Template: fmt.Sprintf("%s_%s", t, firstOr(points, discipline)), Prior: 0.005,
				})
			}
		}
	}
	return out
}

func nextSyntheticPointSet(discipline string, t models.QuestionType, usedKeys map[string]bool, seed int) []string {
	bases := fallbackPointSets(discipline)
	for i := 0; i < 100; i++ {
		var points []string
		suffix := fmt.Sprintf("难度目标候选_%s_%d", t, seed+i)
		if len(bases) > 0 {
			base := bases[(seed+i)%len(bases)]
			points = append(append([]string(nil), base...), suffix)
		} else if discipline == "" {
			points = []string{fmt.Sprintf("%s_auto_%d", t, seed+i)}
		} else {
			// Preserve the caller's namespace for non-math2 profiles. Math2
			// uses the canonical bases above, so this branch is only a generic
			// fallback for a discipline unknown to the local syllabus table.
			points = []string{discipline, suffix}
		}
		key := candidateKey(candidateSpec{Type: t, Points: points})
		if !usedKeys[key] {
			usedKeys[key] = true
			return points
		}
	}
	return nil
}

func countCandidates(cands []candidateSpec, t models.QuestionType, score float64, band models.DifficultyBand) int {
	count := 0
	for _, candidate := range cands {
		if candidate.Type == t && math.Abs(candidate.Score-score) <= 0.01 && candidate.DiffBand == band {
			count++
		}
	}
	return count
}

func augmentDifficultyPool(pool []candidateSpec, bp *Blueprint) []candidateSpec {
	out := append([]candidateSpec(nil), pool...)
	seen := make(map[string]bool, len(out))
	for _, candidate := range out {
		seen[candidateKey(candidate)] = true
	}
	groups := make(map[string][]candidateSpec)
	groupTypes := make(map[string]models.QuestionType)
	groupDisciplines := make(map[string]string)
	for _, candidate := range pool {
		discipline := ""
		if len(bp.DisciplineQuota) > 0 {
			discipline = disciplineForPoints(candidate.Points)
		}
		key := string(candidate.Type) + "\x00" + discipline
		groups[key] = append(groups[key], candidate)
		groupTypes[key] = candidate.Type
		groupDisciplines[key] = discipline
	}
	groupKeys := make([]string, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Strings(groupKeys)
	for _, key := range groupKeys {
		candidates := groups[key]
		scoreSet := make(map[float64]bool)
		scores := make([]float64, 0)
		for _, candidate := range candidates {
			if candidate.Score > 0 && !scoreSet[candidate.Score] {
				scoreSet[candidate.Score] = true
				scores = append(scores, candidate.Score)
			}
		}
		sort.Float64s(scores)
		augmented := augmentDifficultyCandidates(candidates, groupTypes[key], scores, bp, groupDisciplines[key], pool)
		for _, candidate := range augmented {
			candidateKeyValue := candidateKey(candidate)
			if !seen[candidateKeyValue] {
				seen[candidateKeyValue] = true
				out = append(out, candidate)
			}
		}
	}
	return out
}

// pointSetCatalogue returns real, subject-scoped point paths that can be
// reused when a historical score pattern needs a count-only top-up. A point
// path may have appeared under another question type; it is still a valid
// syllabus target, while candidateKey prevents same-type duplication.
func pointSetCatalogue(cands []candidateSpec, discipline string) [][]string {
	seen := make(map[string]bool)
	var out [][]string
	for _, c := range cands {
		if discipline != "" && disciplineForPoints(c.Points) != discipline {
			continue
		}
		if len(c.Points) == 0 {
			continue
		}
		points := append([]string(nil), c.Points...)
		key := strings.Join(points, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, points)
	}
	return out
}

func appendUniquePointSets(base, extra [][]string) [][]string {
	seen := make(map[string]bool, len(base)+len(extra))
	for _, points := range base {
		seen[strings.Join(points, "\x00")] = true
	}
	out := append([][]string(nil), base...)
	for _, points := range extra {
		key := strings.Join(points, "\x00")
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, append([]string(nil), points...))
	}
	return out
}

func nextPointSet(catalogue [][]string, usedKeys map[string]bool, t models.QuestionType, seed int) []string {
	if len(catalogue) == 0 {
		return nil
	}
	for offset := 0; offset < len(catalogue); offset++ {
		points := catalogue[(seed+offset)%len(catalogue)]
		candidate := candidateSpec{Type: t, Points: points}
		if usedKeys[candidateKey(candidate)] {
			continue
		}
		usedKeys[candidateKey(candidate)] = true
		return append([]string(nil), points...)
	}
	return nil
}

func betterState(prior float64, selection []candidateSpec, otherPrior float64, otherSelection []candidateSpec) bool {
	if math.Abs(prior-otherPrior) > 1e-12 {
		return prior > otherPrior
	}
	return selectionKey(selection) < selectionKey(otherSelection)
}

func selectionKey(selection []candidateSpec) string {
	keys := make([]string, 0, len(selection))
	for _, candidate := range selection {
		keys = append(keys, candidateKey(candidate))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x00")
}

// These are only a last resort for an empty historical catalogue. They are
// canonical math2 syllabus paths, never generated marker strings.
func fallbackPointSets(discipline string) [][]string {
	switch discipline {
	case "高等数学":
		return [][]string{
			{"高等数学", "函数", "极限", "连续", "函数极限的计算"},
			{"高等数学", "一元函数微分学", "导数与微分的计算"},
			{"高等数学", "一元函数积分学", "定积分的计算"},
			{"高等数学", "多元函数微分学", "偏导数的概念与计算"},
			{"高等数学", "多元函数积分学", "重积分的计算"},
			{"高等数学", "常微分方程", "常系数齐次线性微分方程"},
		}
	case "线性代数":
		return [][]string{
			{"线性代数", "行列式"},
			{"线性代数", "矩阵", "矩阵的运算与变换"},
			{"线性代数", "向量", "向量组的线性相关性"},
			{"线性代数", "线性方程组"},
			{"线性代数", "矩阵的特征值与特征向量"},
			{"线性代数", "二次型"},
			{"线性代数", "二次型", "矩阵的特征值与特征向量"},
			{"线性代数", "矩阵", "矩阵的秩", "秩的性质"},
			{"线性代数", "向量", "向量组的秩"},
			{"线性代数", "特征值与特征向量"},
			{"线性代数", "二次型", "正交变换化标准形"},
			{"线性代数", "矩阵", "初等变换"},
		}
	default:
		return nil
	}
}

func fixedScorePattern(scores []float64, count int, quota float64) []float64 {
	if count <= 0 {
		return nil
	}
	target := int(math.Round(quota * 2))
	if target <= 0 {
		return nil
	}
	type cell struct {
		used  bool
		prevS int
		prevK int
		score int
	}
	dp := make([][]cell, count+1)
	for k := range dp {
		dp[k] = make([]cell, target+1)
	}
	dp[0][0].used = true
	for k := 0; k < count; k++ {
		for sum := 0; sum <= target; sum++ {
			if !dp[k][sum].used {
				continue
			}
			for _, score := range scores {
				q2 := int(math.Round(score * 2))
				if q2 <= 0 || sum+q2 > target || dp[k+1][sum+q2].used {
					continue
				}
				dp[k+1][sum+q2] = cell{used: true, prevS: sum, prevK: k, score: q2}
			}
		}
	}
	if !dp[count][target].used {
		return nil
	}
	pattern := make([]float64, count)
	sum, k := target, count
	for k > 0 {
		c := dp[k][sum]
		pattern[k-1] = float64(c.score) / 2
		sum, k = c.prevS, c.prevK
	}
	return pattern
}

// buildCandidatePool generates the candidate spec entries from past questions,
// deduplicated by (type, knowledge-point set). The highest-prior variant of a
// duplicate wins; ties keep the first seen (input order), keeping the pool
// deterministic. Without this, the same knowledge points could enter the
// paper twice (e.g. the same point set tagged apply/easy in one year and
// analyze/medium in another), which the intra-paper dedup check then flags.
func (o *Orchestrator) buildCandidatePool(past []models.PastQuestion) []candidateSpec {
	best := make(map[string]candidateSpec)
	order := make([]string, 0, len(past))

	for _, q := range past {
		band := bandFromDifficulty(q.Difficulty)
		tpl := q.TemplateKey
		if tpl == "" {
			tpl = fmt.Sprintf("%s_%s", q.Type, firstOr(q.Points, "default"))
		}
		cand := candidateSpec{
			Type:      q.Type,
			Points:    q.Points,
			Cognitive: q.Cognitive,
			DiffBand:  band,
			Score:     q.Score,
			Template:  tpl,
		}
		cand.Prior = o.lookupPrior(cand)

		k := candidateKey(cand)
		if existing, ok := best[k]; ok {
			if cand.Prior > existing.Prior {
				best[k] = cand
			}
			continue
		}
		best[k] = cand
		order = append(order, k)
	}

	pool := make([]candidateSpec, 0, len(order))
	for _, k := range order {
		pool = append(pool, best[k])
	}
	return pool
}

func poolByType(pool []candidateSpec, t models.QuestionType) []candidateSpec {
	var out []candidateSpec
	for _, c := range pool {
		if c.Type == t {
			out = append(out, c)
		}
	}
	return out
}

// exactFillOptions enumerates, for every achievable question count, the
// highest-prior selection of candidates whose scores sum exactly to quota.
// Scores are quantised to 0.5分 granularity for the DP (real exam scores are
// integers). Returns nil when the quota is unreachable with these candidates.
//
// Each DP cell stores its selection snapshot (candidate indices). Snapshots
// are immutable and only ever built from cells written by earlier items, so
// a selection can never contain the same candidate twice — pointer-chasing
// reconstruction on an in-place table can, which would put the same
// knowledge points into the paper repeatedly.
func exactFillOptions(cands []candidateSpec, quota float64) map[int][]candidateSpec {
	Q := int(math.Round(quota * 2))
	if Q <= 0 || len(cands) == 0 {
		return nil
	}

	// Highest-prior candidates first, capped for DP size.
	items := make([]candidateSpec, len(cands))
	copy(items, cands)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Prior > items[j].Prior })
	if len(items) > 512 {
		items = items[:512]
	}
	type dpItem struct {
		q2 int
		c  candidateSpec
	}
	dpItems := make([]dpItem, 0, len(items))
	for _, c := range items {
		q2 := int(math.Round(c.Score * 2))
		if q2 > 0 && q2 <= Q {
			dpItems = append(dpItems, dpItem{q2, c})
		}
	}
	if len(dpItems) == 0 {
		return nil
	}

	maxK := 100
	if len(dpItems) < maxK {
		maxK = len(dpItems)
	}

	const width = 101 // k dimension: 0..maxK, maxK ≤ 100
	infeasible := math.Inf(1)
	type cell struct {
		prior float64
		sel   []int32
	}
	dp := make([]cell, (Q+1)*width)
	for i := range dp {
		dp[i].prior = infeasible
	}
	idx := func(s, k int) int { return s*width + k }
	dp[idx(0, 0)] = cell{0, []int32{}}

	for ii, it := range dpItems {
		for s := Q - it.q2; s >= 0; s-- {
			for k := maxK - 1; k >= 0; k-- {
				cur := dp[idx(s, k)]
				if math.IsInf(cur.prior, 1) {
					continue
				}
				tgt := idx(s+it.q2, k+1)
				next := cur.prior + it.c.Prior
				// Unreachable cells hold +Inf — writing there always wins.
				if math.IsInf(dp[tgt].prior, 1) || next > dp[tgt].prior {
					sel := make([]int32, len(cur.sel)+1)
					copy(sel, cur.sel)
					sel[len(sel)-1] = int32(ii)
					dp[tgt] = cell{next, sel}
				}
			}
		}
	}

	out := make(map[int][]candidateSpec)
	for k := 1; k <= maxK; k++ {
		c := dp[idx(Q, k)]
		if math.IsInf(c.prior, 1) {
			continue
		}
		sel := make([]candidateSpec, 0, len(c.sel))
		for _, ii := range c.sel {
			sel = append(sel, dpItems[ii].c)
		}
		out[k] = sel
	}
	return out
}

// augmentType tops up an infeasible candidate pool with synthetic candidates
// at the pool's modal score so the quota becomes exactly reachable. Points are
// unique per synthetic candidate so two of them can never collide in one
// paper (intra-paper Jaccard 1.0).
func augmentType(cands []candidateSpec, t models.QuestionType, quota float64, bp *Blueprint) []candidateSpec {
	mode := modalScore(cands)
	if mode <= 0 {
		mode = typicalScore(t)
	}
	need := int(math.Ceil(quota/mode)) + 2
	if need < 1 {
		need = 1
	}
	bands := []models.DifficultyBand{models.BandEasy, models.BandMedium, models.BandHard}
	out := cands
	// Preserve the score granularity found in the corpus. This matters for
	// subjects whose historic format changed (e.g. math choice questions can
	// be 4 or 5 points): topping up with only the modal score can make an
	// otherwise valid 50-point quota arithmetically unreachable.
	scores := make([]float64, 0, len(cands)+1)
	seenScores := make(map[float64]bool)
	for _, c := range cands {
		if c.Score > 0 && !seenScores[c.Score] {
			seenScores[c.Score] = true
			scores = append(scores, c.Score)
		}
	}
	if len(scores) == 0 {
		scores = append(scores, mode)
	}
	for i := 0; i < need*len(scores); i++ {
		score := scores[i%len(scores)]
		out = append(out, candidateSpec{
			Type:      t,
			Points:    []string{fmt.Sprintf("%s_auto_%d", t, i)},
			Cognitive: pickCognitive(bp),
			DiffBand:  bands[i%len(bands)],
			Score:     score,
			Template:  fmt.Sprintf("%s_default", t),
			Prior:     0.1, // low prior for synthetic
		})
	}
	return out
}

// modalScore returns the most common candidate score (larger score wins
// ties), so synthetic top-ups match what the subject actually uses.
func modalScore(cands []candidateSpec) float64 {
	count := make(map[float64]int)
	best, bestN := 0.0, 0
	for _, c := range cands {
		count[c.Score]++
		if count[c.Score] > bestN || (count[c.Score] == bestN && c.Score > best) {
			best, bestN = c.Score, count[c.Score]
		}
	}
	return best
}

// greedyType is the best-effort fallback when a quota cannot be exactly met:
// pick highest-prior candidates while they fit under the quota.
func greedyType(cands []candidateSpec, quota float64) []candidateSpec {
	sorted := make([]candidateSpec, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Prior > sorted[j].Prior })
	var sel []candidateSpec
	remaining := quota
	for _, c := range sorted {
		if c.Score > remaining+0.01 {
			continue
		}
		sel = append(sel, c)
		remaining -= c.Score
		if remaining < 0.01 {
			break
		}
	}
	return sel
}

// combineCounts selects one count option per type so the total question count
// approaches NumQuestions (soft target; 0 = unconstrained), maximising the
// prior sum as tie-break. Branch-and-bound pruning: once a combination with
// delta d is found, paths already overshooting the best delta are cut — but
// nothing is pruned before a first feasible combination exists, so a pool
// whose minimum count exceeds NumQuestions still yields the closest one.
func combineCounts(options map[models.QuestionType]map[int][]candidateSpec, types []models.QuestionType, numTarget int) []candidateSpec {
	var best []candidateSpec
	bestDelta := math.MaxInt
	var bestPrior float64

	var walk func(i, count int, prior float64, sel []candidateSpec)
	walk = func(i, count int, prior float64, sel []candidateSpec) {
		if i == len(types) {
			delta := 0
			if numTarget > 0 {
				delta = count - numTarget
				if delta < 0 {
					delta = -delta
				}
			}
			if delta < bestDelta || (delta == bestDelta && prior > bestPrior) {
				bestDelta, bestPrior = delta, prior
				best = append([]candidateSpec(nil), sel...)
			}
			return
		}
		opts := options[types[i]]
		ks := make([]int, 0, len(opts))
		for k := range opts {
			ks = append(ks, k)
		}
		sort.Ints(ks)
		for _, k := range ks {
			next := count + k
			if numTarget > 0 {
				d := next - numTarget
				if d < 0 {
					d = -d
				}
				// Deeper counts only grow — cut when already worse than the
				// best combination found so far. Equal-delta paths stay
				// explorable so the higher-prior tie-break can win.
				if bestDelta != math.MaxInt && d > bestDelta {
					continue
				}
			}
			sel := append(sel, opts[k]...)
			walk(i+1, next, prior+sumScores(opts[k]), sel)
			sel = sel[:len(sel)-len(opts[k])]
		}
	}
	walk(0, 0, 0, nil)
	return best
}

// selectFixedCounts applies an official subject structure after per-type
// score filling. Score quotas alone are insufficient when the historical
// corpus mixes exam formats, such as 4-point and 5-point choices.
func selectFixedCounts(options map[models.QuestionType]map[int][]candidateSpec, types []models.QuestionType, counts map[models.QuestionType]int, numTarget int) ([]candidateSpec, error) {
	if len(counts) != len(types) {
		return nil, fmt.Errorf("fixed type-count quota must cover every configured question type")
	}
	total := 0
	selection := make([]candidateSpec, 0)
	for _, t := range types {
		want, ok := counts[t]
		if !ok || want <= 0 {
			return nil, fmt.Errorf("fixed type-count quota for %s must be positive", t)
		}
		option, ok := options[t][want]
		if !ok {
			return nil, fmt.Errorf("cannot satisfy %s quota with exactly %d questions", t, want)
		}
		selection = append(selection, option...)
		total += want
	}
	if numTarget > 0 && total != numTarget {
		return nil, fmt.Errorf("fixed type-count quotas total %d questions, want %d", total, numTarget)
	}
	return selection, nil
}

// candidateKey identifies a pool candidate within one paper: type plus its
// canonical (sorted) knowledge-point set. For untagged corpora, the parser
// uses the placeholder "unknown"; use the structural template as a fallback
// there so every English section is not collapsed into one candidate.
func candidateKey(c candidateSpec) string {
	pts := make([]string, len(c.Points))
	copy(pts, c.Points)
	sort.Strings(pts)
	key := string(c.Type) + "|" + strings.Join(pts, ",")
	if len(pts) == 1 && pts[0] == "unknown" && c.Template != "" {
		key += "|" + c.Template
	}
	return key
}

// localSearch improves the selection by swapping candidates to reduce the
// B-layer penalty. Single swaps preserve score and type; pair swaps preserve
// the combined score, type, and discipline. Pair swaps are important when a
// fixed-count cell has mixed historical scores (for example 10+12-point
// major questions): a one-for-one same-score swap cannot reach another valid
// score composition even when the requested difficulty mix is feasible.
func (o *Orchestrator) localSearch(pool []candidateSpec, selection []candidateSpec, bp *Blueprint) []candidateSpec {
	const maxIter = 50
	currentPenalty := o.totalPenalty(selection, bp)

	usage := make(map[string]int, len(selection))
	for _, c := range selection {
		usage[candidateKey(c)]++
	}

	for iter := 0; iter < maxIter; iter++ {
		improved := false
		for i := 0; i < len(selection); i++ {
			old := selection[i]
			oldKey := candidateKey(old)
			for _, cand := range pool {
				if cand.Score != old.Score || cand.Type != old.Type {
					continue // swap must preserve score and type to keep quota sums
				}
				if len(bp.DisciplineQuota) > 0 && disciplineForPoints(cand.Points) != disciplineForPoints(old.Points) {
					continue // preserve hard discipline quotas during local search
				}
				ck := candidateKey(cand)
				if ck != oldKey && usage[ck] > 0 {
					continue // already in the paper — never duplicate
				}
				selection[i] = cand
				newPenalty := o.totalPenalty(selection, bp)
				if newPenalty < currentPenalty {
					currentPenalty = newPenalty
					usage[oldKey]--
					usage[ck]++
					improved = true
					break // keep this swap, move to next i
				}
				selection[i] = old // revert
			}
			if improved {
				break
			}
		}
		if !improved {
			for i := 0; i < len(selection) && !improved; i++ {
				for j := i + 1; j < len(selection) && !improved; j++ {
					oldI, oldJ := selection[i], selection[j]
					if oldI.Type != oldJ.Type {
						continue
					}
					disciplineI := disciplineForPoints(oldI.Points)
					disciplineJ := disciplineForPoints(oldJ.Points)
					sameGroup := len(bp.DisciplineQuota) == 0 || disciplineI == disciplineJ
					oldSum := oldI.Score + oldJ.Score
					oldKeyI, oldKeyJ := candidateKey(oldI), candidateKey(oldJ)
					allowed := map[string]int{oldKeyI: 1, oldKeyJ: 1}
					for _, candI := range pool {
						if candI.Type != oldI.Type || !sameDiscipline(candI, oldI, bp) {
							continue
						}
						keyI := candidateKey(candI)
						if usage[keyI]-allowed[keyI] > 0 {
							continue
						}
						for _, candJ := range pool {
							if candJ.Type != oldJ.Type || !sameDiscipline(candJ, oldJ, bp) {
								continue
							}
							if sameGroup {
								if math.Abs(candI.Score+candJ.Score-oldSum) > 0.01 {
									continue
								}
							} else if math.Abs(candI.Score-oldI.Score) > 0.01 || math.Abs(candJ.Score-oldJ.Score) > 0.01 {
								continue
							}
							keyJ := candidateKey(candJ)
							if keyJ == keyI || usage[keyJ]-allowed[keyJ] > 0 {
								continue
							}
							selection[i], selection[j] = candI, candJ
							newPenalty := o.totalPenalty(selection, bp)
							if newPenalty < currentPenalty {
								currentPenalty = newPenalty
								usage[oldKeyI]--
								usage[oldKeyJ]--
								usage[keyI]++
								usage[keyJ]++
								improved = true
								break
							}
							selection[i], selection[j] = oldI, oldJ
						}
					}
				}
			}
			if !improved {
				break
			}
		}
	}
	return selection
}

func sameDiscipline(candidate, reference candidateSpec, bp *Blueprint) bool {
	return len(bp.DisciplineQuota) == 0 || disciplineForPoints(candidate.Points) == disciplineForPoints(reference.Points)
}

// totalPenalty computes the B-layer soft-target violation penalty.
// Lower is better. Components:
//   - Difficulty histogram deviation (sum of |actual - target| per band)
//   - Usage cap excess (sum of max(0, count - cap) per point)
//   - Prior bonus (negative penalty: higher prior = lower penalty)
func (o *Orchestrator) totalPenalty(selection []candidateSpec, bp *Blueprint) float64 {
	// Difficulty histogram deviation. The integer target is the primary signal:
	// a ratio such as 10/45/45 cannot be represented exactly by every paper
	// length, so comparing only fractions can leave the nearest valid slot
	// assignment under-specified.
	bandCounts := make(map[models.DifficultyBand]int)
	for _, c := range selection {
		bandCounts[c.DiffBand]++
	}
	total := len(selection)
	if total == 0 {
		return math.Inf(1)
	}
	histPenalty := 0.0
	for band, targetFrac := range bp.DiffHist {
		actualFrac := float64(bandCounts[band]) / float64(total)
		histPenalty += math.Abs(actualFrac - targetFrac)
	}
	difficultyCountPenalty := 0.0
	if targets := DifficultyTargetCounts(bp, total); bp.DifficultyCountStrict && len(targets) > 0 {
		for _, band := range difficultyBands {
			difficultyCountPenalty += math.Abs(float64(bandCounts[band] - targets[band]))
		}
	}

	// Usage cap excess.
	capPenalty := 0.0
	pointCount := make(map[string]int)
	for _, c := range selection {
		for _, pt := range c.Points {
			pointCount[pt]++
		}
	}
	for pt, count := range pointCount {
		if cap, ok := bp.UsageCap[pt]; ok {
			if count > cap {
				capPenalty += float64(count - cap)
			}
		}
	}

	// Prior bonus (subtract: higher prior → lower penalty).
	priorBonus := 0.0
	for _, c := range selection {
		priorBonus += c.Prior
	}

	// Count targets are deliberately dominant, while the fractional histogram
	// and GNN prior still break ties among equally accurate compositions.
	return difficultyCountPenalty*10 + histPenalty + capPenalty - priorBonus*0.5
}

// lookupPrior looks up the GNN weight for a candidate's (point, type, cognitive).
func (o *Orchestrator) lookupPrior(c candidateSpec) float64 {
	if o.GNNWeights == nil {
		return 0.3 // default prior when no GNN
	}
	total := 0.0
	count := 0
	for _, pt := range c.Points {
		inner, ok := o.GNNWeights[pt]
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s_%s", c.Type, c.Cognitive)
		if w, ok := inner[key]; ok {
			total += w
			count++
		}
	}
	if count == 0 {
		return 0.2
	}
	return total / float64(count)
}

// pickCognitive selects a cognitive level for synthetic candidates, respecting
// BloomQuota (deterministic: sorted keys, strict-improvement comparison).
func pickCognitive(bp *Blueprint) models.CognitiveLevel {
	best := models.CogApply
	bestQuota := -1
	cogs := make([]models.CognitiveLevel, 0, len(bp.BloomQuota))
	for cog := range bp.BloomQuota {
		cogs = append(cogs, cog)
	}
	sort.Slice(cogs, func(i, j int) bool { return cogs[i] < cogs[j] })
	for _, cog := range cogs {
		if quota := bp.BloomQuota[cog]; quota > bestQuota {
			bestQuota = quota
			best = cog
		}
	}
	return best
}

// --- Helpers ---

var difficultyBands = []models.DifficultyBand{
	models.BandEasy,
	models.BandMedium,
	models.BandHard,
}

// AllocateDifficultyCounts converts a normalized difficulty histogram into
// integer question counts. Largest-remainder allocation keeps the total exact
// and makes ties deterministic in easy -> medium -> hard order.
func AllocateDifficultyCounts(hist map[models.DifficultyBand]float64, total int) (map[models.DifficultyBand]int, error) {
	if total < 0 {
		return nil, fmt.Errorf("difficulty total cannot be negative")
	}
	if len(hist) == 0 {
		return nil, nil
	}
	totalFraction := 0.0
	for _, band := range difficultyBands {
		value := hist[band]
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return nil, fmt.Errorf("difficulty ratio must be a finite non-negative number")
		}
		totalFraction += value
	}
	if math.Abs(totalFraction-1) > 1e-6 {
		return nil, fmt.Errorf("difficulty ratios must sum to 1, got %.6f", totalFraction)
	}

	counts := make(map[models.DifficultyBand]int, len(difficultyBands))
	type remainder struct {
		band      models.DifficultyBand
		remainder float64
	}
	remainders := make([]remainder, 0, len(difficultyBands))
	allocated := 0
	for _, band := range difficultyBands {
		exact := hist[band] * float64(total)
		whole := int(math.Floor(exact + 1e-12))
		counts[band] = whole
		allocated += whole
		remainders = append(remainders, remainder{band: band, remainder: exact - float64(whole)})
	}
	sort.SliceStable(remainders, func(i, j int) bool {
		if math.Abs(remainders[i].remainder-remainders[j].remainder) > 1e-12 {
			return remainders[i].remainder > remainders[j].remainder
		}
		return remainders[i].band < remainders[j].band
	})
	for i := 0; allocated < total; i++ {
		counts[remainders[i%len(remainders)].band]++
		allocated++
	}
	return counts, nil
}

// DifficultyTargetCounts returns the integer target used by composition and
// validation. Explicit counts are authoritative when they match total;
// otherwise the histogram is converted with largest-remainder allocation.
func DifficultyTargetCounts(bp *Blueprint, total int) map[models.DifficultyBand]int {
	if bp == nil || total < 0 {
		return nil
	}
	if len(bp.DiffCountQuota) > 0 {
		sum := 0
		counts := make(map[models.DifficultyBand]int, len(difficultyBands))
		valid := true
		for _, band := range difficultyBands {
			value := bp.DiffCountQuota[band]
			if value < 0 {
				valid = false
				break
			}
			counts[band] = value
			sum += value
		}
		if valid && sum == total {
			return counts
		}
	}
	counts, err := AllocateDifficultyCounts(bp.DiffHist, total)
	if err != nil {
		return nil
	}
	return counts
}

func bandFromDifficulty(d float64) models.DifficultyBand {
	return models.BandFromDifficulty(d)
}

func typicalScore(t models.QuestionType) float64 {
	switch t {
	case models.TypeChoice, models.TypeMultiChoice:
		return 2
	case models.TypeFillBlank:
		return 4
	default:
		return 10
	}
}

func firstOr(ss []string, def string) string {
	if len(ss) > 0 && ss[0] != "" {
		return ss[0]
	}
	return def
}

func sumScores(cs []candidateSpec) float64 {
	total := 0.0
	for _, c := range cs {
		total += c.Score
	}
	return total
}
