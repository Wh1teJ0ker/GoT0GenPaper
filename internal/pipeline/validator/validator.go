// Package validator implements Stage [6]: 整卷校验层.
//
// Single-question pass ≠ whole-paper达标. This stage verifies:
//   - 覆盖完整性 (all required points covered)
//   - 难度校准 (measured vs blueprint, KL > ε triggers re-spec)
//   - 无重复 (intra-paper + vs real-exam + vs generated-pool)
//   - 分值总和
//
// Repair protocol: only re-spec+regenerate violated题位, ≤3 rounds,
// then degrade to candidate-pool selection.
package validator

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/choice"
	"GoT0GenPaper/internal/pipeline/orchestrator"
	"GoT0GenPaper/internal/pipeline/quality"
)

// KLEpsilon: difficulty histogram KL divergence threshold for re-spec.
const KLEpsilon = 0.15

// SimilarityThreshold: above this, two questions are considered duplicates.
const SimilarityThreshold = 0.85

// Validator runs whole-paper validation checks.
type Validator struct {
	PastExamDB    []models.PastQuestion      // 去重基准库
	GeneratedPool []models.GeneratedQuestion // 已生成题库
}

// New creates a Validator.
func New(pastDB []models.PastQuestion) *Validator {
	return &Validator{PastExamDB: pastDB}
}

// ValidationResult reports per-check pass/fail.
type ValidationResult struct {
	StructureOK            bool           `json:"structureOK"`
	StructureIssues        []string       `json:"structureIssues,omitempty"`
	ContentReady           bool           `json:"contentReady"`
	ContentIssues          []string       `json:"contentIssues,omitempty"`
	CoverageOK             bool           `json:"coverageOK"`
	DifficultyOK           bool           `json:"difficultyOK"`
	DifficultyCountsTarget map[string]int `json:"difficultyCountsTarget,omitempty"`
	DifficultyCountsActual map[string]int `json:"difficultyCountsActual,omitempty"`
	NoDuplicates           bool           `json:"noDuplicates"`
	ScoreTotalOK           bool           `json:"scoreTotalOK"`
	ViolatedSpecIDs        []string       `json:"violatedSpecIds"`
	KL                     float64        `json:"kl"` // difficulty histogram KL divergence
	DuplicatePairs         []string       `json:"duplicatePairs"`
	MissingPoints          []string       `json:"missingPoints"`
	ScoreActual            float64        `json:"scoreActual"`
	ScoreExpected          float64        `json:"scoreExpected"`
}

// Validate runs all whole-paper checks.
//
// bp is expected to be *orchestrator.Blueprint (kept as interface{} to avoid
// a hard dependency cycle in some wiring scenarios).
func (v *Validator) Validate(ctx context.Context, paper models.ExamPaper, bp interface{}) (*ValidationResult, error) {
	result := &ValidationResult{
		StructureOK:     true,
		ViolatedSpecIDs: []string{},
		DuplicatePairs:  []string{},
		MissingPoints:   []string{},
	}
	result.StructureIssues = paperStructureIssues(paper)
	result.StructureOK = len(result.StructureIssues) == 0
	result.ContentIssues = paperContentIssues(paper)
	result.ContentReady = len(result.ContentIssues) == 0

	// 1. Coverage check: all blueprint coverage-floor points must be present.
	coveredPoints := make(map[string]float64) // point → accumulated score
	for _, q := range paper.Questions {
		for _, pt := range q.Points {
			coveredPoints[pt] += q.Difficulty // use difficulty as proxy weight
		}
	}
	// Also use score as coverage metric.
	coveredByScore := make(map[string]float64)
	for _, q := range paper.Questions {
		for _, pt := range q.Points {
			coveredByScore[pt] += paperTotalQuestionScore(q)
		}
	}

	if bp != nil {
		if blueprint, ok := bp.(*orchestrator.Blueprint); ok {
			result.StructureIssues = append(result.StructureIssues, blueprintStructureIssues(paper, blueprint)...)
			result.StructureOK = len(result.StructureIssues) == 0
			for pt, floor := range blueprint.CoverageFloor {
				if coveredByScore[pt] < floor {
					result.MissingPoints = append(result.MissingPoints, pt)
				}
			}
		}
	}
	result.CoverageOK = len(paper.Questions) > 0 && len(result.MissingPoints) == 0

	// 2. Difficulty histogram check: compute KL divergence vs blueprint.
	bandCounts := make(map[models.DifficultyBand]int)
	for _, q := range paper.Questions {
		band := bandFromDifficulty(q.Difficulty)
		bandCounts[band]++
	}
	total := len(paper.Questions)
	if total == 0 {
		total = 1
	}
	// Build actual distribution.
	actualDist := make(map[models.DifficultyBand]float64)
	for band, count := range bandCounts {
		actualDist[band] = float64(count) / float64(total)
	}
	// Build target distribution from blueprint.
	targetDist := make(map[models.DifficultyBand]float64)
	if bp != nil {
		if blueprint, ok := bp.(*orchestrator.Blueprint); ok {
			for band, frac := range blueprint.DiffHist {
				targetDist[band] = frac
			}
		}
	}
	if len(targetDist) == 0 {
		// Default target: 30% easy, 50% medium, 20% hard.
		targetDist[models.BandEasy] = 0.3
		targetDist[models.BandMedium] = 0.5
		targetDist[models.BandHard] = 0.2
	}
	kl := klDivergence(actualDist, targetDist)
	result.KL = kl
	actualCounts := map[models.DifficultyBand]int{
		models.BandEasy:   bandCounts[models.BandEasy],
		models.BandMedium: bandCounts[models.BandMedium],
		models.BandHard:   bandCounts[models.BandHard],
	}
	targetCounts := map[models.DifficultyBand]int(nil)
	difficultyTargetStrict := paper.SpecTable.DifficultyTargetStrict
	if blueprint, ok := bp.(*orchestrator.Blueprint); ok && blueprint != nil {
		targetCounts = orchestrator.DifficultyTargetCounts(blueprint, len(paper.Questions))
		difficultyTargetStrict = blueprint.DifficultyCountStrict
	}
	if len(targetCounts) == 0 && len(paper.SpecTable.DifficultyTarget) > 0 {
		targetCounts = paper.SpecTable.DifficultyTarget
	}
	if len(targetCounts) == 0 {
		fallback, _ := orchestrator.AllocateDifficultyCounts(targetDist, len(paper.Questions))
		targetCounts = fallback
	}
	result.DifficultyCountsActual = difficultyCountView(actualCounts)
	result.DifficultyCountsTarget = difficultyCountView(targetCounts)
	countsMatch := !difficultyTargetStrict || len(targetCounts) == 0 || countsEqual(actualCounts, targetCounts)
	result.DifficultyOK = len(paper.Questions) > 0 && countsMatch && kl < KLEpsilon

	// 3. Dedup check: intra-paper + vs past-exam + vs generated-pool.
	dupPairs := v.checkDuplicates(paper)
	result.DuplicatePairs = dupPairs
	result.NoDuplicates = len(dupPairs) == 0

	// 4. Total score check — against the blueprint total when one is supplied.
	// Comparing against paper.TotalScore would be tautological: buildPaper
	// derives that field from the same per-question sum.
	actualScore := 0.0
	for _, q := range paper.Questions {
		actualScore += paperTotalQuestionScore(q)
	}
	result.ScoreActual = actualScore
	result.ScoreExpected = paper.TotalScore
	if bp != nil {
		if blueprint, ok := bp.(*orchestrator.Blueprint); ok && blueprint != nil && blueprint.TotalScore > 0 {
			result.ScoreExpected = blueprint.TotalScore
		}
	}
	result.ScoreTotalOK = len(paper.Questions) > 0 && math.Abs(actualScore-result.ScoreExpected) < 0.5

	// 5. Collect violated spec IDs for repair. repairPaper maps these IDs back
	// to SpecTable entries, so every violation must be reported as a SpecID
	// (question IDs like "gen_spec_007" are not mappable) and reported once.
	qToSpec := make(map[string]string, len(paper.Questions))
	for _, q := range paper.Questions {
		if q.SpecID != "" {
			qToSpec[q.ID] = q.SpecID
		}
	}
	seenSpec := make(map[string]bool)
	addViolation := func(questionID string) {
		if strings.TrimSpace(questionID) == "" {
			return
		}
		id, ok := qToSpec[questionID]
		if !ok {
			// Already a spec ID, or an ID with no spec mapping.
			if !strings.HasPrefix(questionID, "gen_") {
				id = questionID
			} else {
				return
			}
		}
		if !seenSpec[id] {
			seenSpec[id] = true
			result.ViolatedSpecIDs = append(result.ViolatedSpecIDs, id)
		}
	}
	for _, q := range paper.Questions {
		if len(quality.SubjectContentIssues(paper.Subject, q.Points, questionContent(q))) > 0 {
			addViolation(q.SpecID)
		}
	}
	if !result.CoverageOK {
		// A missing point is absent from the generated questions, so looking at
		// question.Points cannot identify the slot that should be repaired. The
		// pre-generation spec table still contains that mapping.
		for _, missing := range result.MissingPoints {
			for _, entry := range paper.SpecTable.Entries {
				if contains(entry.Points, missing) {
					addViolation(entry.ID)
					break
				}
			}
		}
	}
	if !result.NoDuplicates {
		for _, pair := range dupPairs {
			// pair format: "idA ~ idB"
			parts := strings.SplitN(pair, " ~ ", 2)
			if len(parts) == 2 {
				addViolation(parts[0])
				addViolation(strings.Fields(parts[1])[0])
			}
		}
	}
	if !result.DifficultyOK {
		for _, id := range difficultyRepairSlots(paper.Questions, paper.SpecTable.Entries) {
			addViolation(id)
		}
	}
	if !result.StructureOK || !result.ContentReady || !result.ScoreTotalOK {
		questionBySpec := make(map[string]models.GeneratedQuestion, len(paper.Questions))
		for _, q := range paper.Questions {
			questionBySpec[q.SpecID] = q
			if q.Status == "needs_review" || !finiteInRange(q.Difficulty, 0, 1) || !positiveFinite(q.Score) ||
				blankOrPlaceholder(q.Stem) || blankOrPlaceholder(q.Answer) || hasIncompleteRubric(paper.Rubrics, q) {
				addViolation(q.SpecID)
			}
		}
		for _, entry := range paper.SpecTable.Entries {
			if _, exists := questionBySpec[entry.ID]; !exists {
				addViolation(entry.ID)
			}
		}
		if !result.ScoreTotalOK && len(result.ViolatedSpecIDs) == 0 {
			for _, entry := range paper.SpecTable.Entries {
				addViolation(entry.ID)
			}
		}
	}

	return result, nil
}

func blueprintStructureIssues(paper models.ExamPaper, blueprint *orchestrator.Blueprint) []string {
	if blueprint == nil {
		return nil
	}
	counts := make(map[models.QuestionType]int)
	scores := make(map[models.QuestionType]float64)
	for _, q := range paper.Questions {
		counts[q.Type]++
		scores[q.Type] += q.Score
	}
	issues := make([]string, 0)
	for questionType, want := range blueprint.TypeCountQuota {
		if counts[questionType] != want {
			issues = append(issues, fmt.Sprintf("题型 %s 数量为 %d，要求 %d", questionType, counts[questionType], want))
		}
	}
	for questionType, want := range blueprint.TypeQuota {
		if math.Abs(scores[questionType]-want) > 0.01 {
			issues = append(issues, fmt.Sprintf("题型 %s 分值为 %.1f，要求 %.1f", questionType, scores[questionType], want))
		}
	}
	if len(blueprint.DisciplineQuota) > 0 {
		disciplineCounts := make(map[string]map[models.QuestionType]int)
		disciplineScores := make(map[string]map[models.QuestionType]float64)
		orderIndex := make(map[string]int, len(blueprint.DisciplineOrder))
		for i, discipline := range blueprint.DisciplineOrder {
			orderIndex[discipline] = i
		}
		lastOrder := make(map[models.QuestionType]int)
		seenType := make(map[models.QuestionType]bool)
		orderIssues := make(map[models.QuestionType]bool)
		for _, q := range paper.Questions {
			discipline := models.DisciplineFromPoints(q.Points)
			if disciplineCounts[discipline] == nil {
				disciplineCounts[discipline] = make(map[models.QuestionType]int)
				disciplineScores[discipline] = make(map[models.QuestionType]float64)
			}
			disciplineCounts[discipline][q.Type]++
			disciplineScores[discipline][q.Type] += q.Score
			if len(blueprint.DisciplineOrder) > 0 {
				currentOrder, ok := orderIndex[discipline]
				if !ok {
					currentOrder = len(orderIndex)
				}
				if seenType[q.Type] && currentOrder < lastOrder[q.Type] {
					orderIssues[q.Type] = true
				}
				seenType[q.Type] = true
				lastOrder[q.Type] = currentOrder
			}
		}

		disciplines := make([]string, 0, len(blueprint.DisciplineQuota))
		for discipline := range blueprint.DisciplineQuota {
			disciplines = append(disciplines, discipline)
		}
		sort.Slice(disciplines, func(i, j int) bool {
			di, okI := orderIndex[disciplines[i]]
			dj, okJ := orderIndex[disciplines[j]]
			if okI != okJ {
				return okI
			}
			if okI && di != dj {
				return di < dj
			}
			return disciplines[i] < disciplines[j]
		})
		questionTypes := make([]models.QuestionType, 0, len(blueprint.TypeQuota))
		for questionType := range blueprint.TypeQuota {
			questionTypes = append(questionTypes, questionType)
		}
		sort.Slice(questionTypes, func(i, j int) bool { return questionTypes[i] < questionTypes[j] })
		for _, discipline := range disciplines {
			for _, questionType := range questionTypes {
				scoreQuota, scoreOK := blueprint.DisciplineQuota[discipline][questionType]
				countQuota, countOK := blueprint.DisciplineCountQuota[discipline][questionType]
				if !scoreOK || !countOK {
					issues = append(issues, fmt.Sprintf("蓝图缺少 %s/%s 学科配额", discipline, questionType))
					continue
				}
				if math.Abs(disciplineScores[discipline][questionType]-scoreQuota) > 0.01 {
					issues = append(issues, fmt.Sprintf("%s 的 %s 分值为 %.1f，要求 %.1f", discipline, questionType, disciplineScores[discipline][questionType], scoreQuota))
				}
				if disciplineCounts[discipline][questionType] != countQuota {
					issues = append(issues, fmt.Sprintf("%s 的 %s 数量为 %d，要求 %d", discipline, questionType, disciplineCounts[discipline][questionType], countQuota))
				}
			}
		}
		for _, questionType := range questionTypes {
			if orderIssues[questionType] {
				issues = append(issues, fmt.Sprintf("题型 %s 的学科顺序不符合蓝图", questionType))
			}
		}
	}
	return issues
}

func difficultyRepairSlots(questions []models.GeneratedQuestion, entries []models.SpecEntry) []string {
	bandBySpec := make(map[string]models.DifficultyBand, len(entries))
	for _, entry := range entries {
		bandBySpec[entry.ID] = entry.DiffBand
	}
	var ids []string
	for _, q := range questions {
		band, exists := bandBySpec[q.SpecID]
		if !exists || strings.TrimSpace(q.SpecID) == "" || !finiteInRange(q.Difficulty, 0, 1) {
			continue
		}
		if math.Abs(q.Difficulty-difficultyBandCenter(band)) > 0.15 {
			ids = append(ids, q.SpecID)
		}
	}
	return ids
}

func difficultyBandCenter(band models.DifficultyBand) float64 {
	switch band {
	case models.BandEasy:
		return 0.25
	case models.BandHard:
		return 0.75
	default:
		return 0.5
	}
}

func difficultyCountView(counts map[models.DifficultyBand]int) map[string]int {
	view := map[string]int{"easy": 0, "medium": 0, "hard": 0}
	for band, name := range map[models.DifficultyBand]string{
		models.BandEasy: "easy", models.BandMedium: "medium", models.BandHard: "hard",
	} {
		view[name] = counts[band]
	}
	return view
}

func countsEqual(a, b map[models.DifficultyBand]int) bool {
	for _, band := range []models.DifficultyBand{models.BandEasy, models.BandMedium, models.BandHard} {
		if a[band] != b[band] {
			return false
		}
	}
	return true
}

func hasIncompleteRubric(rubrics []models.Rubric, q models.GeneratedQuestion) bool {
	for _, rubric := range rubrics {
		if rubric.QuestionID != q.ID {
			continue
		}
		total := 0.0
		for _, item := range rubric.Items {
			if !positiveFinite(item.MaxScore) || strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Description) == "" {
				return true
			}
			total += item.MaxScore
		}
		return len(rubric.Items) == 0 || !finiteInRange(total, q.Score-0.01, q.Score+0.01)
	}
	return true
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteInRange(value, minValue, maxValue float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minValue && value <= maxValue
}

// RepairLoop protocol (re-spec only violated题位, ≤3 rounds, then degrade to
// candidate-pool selection) is orchestrated at the API layer, because repair
// needs the generator (Stage 4) and answer service (Stage 5) — the validator
// stays a pure checker. See api.go repairPaper.

// checkDuplicates finds duplicate questions within the paper and against
// the past-exam database and generated pool.
func (v *Validator) checkDuplicates(paper models.ExamPaper) []string {
	var dupPairs []string

	// Intra-paper duplicates: same knowledge-point set within the same
	// question type. Cross-type overlap (a choice and a major on the same
	// points) is normal exam practice, not duplication.
	for i := 0; i < len(paper.Questions); i++ {
		for j := i + 1; j < len(paper.Questions); j++ {
			if paper.Questions[i].Type != paper.Questions[j].Type {
				continue
			}
			sim := jaccardSimilarity(paper.Questions[i].Points, paper.Questions[j].Points)
			if sim >= SimilarityThreshold {
				dupPairs = append(dupPairs,
					fmt.Sprintf("%s ~ %s (intra, sim=%.2f)",
						paper.Questions[i].ID, paper.Questions[j].ID, sim))
			}
		}
	}

	// vs Past-exam DB. Generated questions inherit their knowledge points
	// from the spec table, which is built out of past exams — a point-set
	// comparison would flag every question as a duplicate of its own source.
	// The past-exam check therefore compares question stems (text similarity)
	// and only when both stems are real content (not offline placeholders).
	for _, q := range paper.Questions {
		if !stemUsable(q.Stem) {
			continue
		}
		for _, past := range v.PastExamDB {
			if !stemUsable(past.Stem) {
				continue
			}
			sim := stemSimilarity(q.Stem, past.Stem)
			if sim >= SimilarityThreshold {
				dupPairs = append(dupPairs,
					fmt.Sprintf("%s ~ %s (past-exam, sim=%.2f)",
						q.ID, past.ID, sim))
			}
		}
	}

	// vs Generated pool (same-type scope, consistent with intra-paper).
	for _, q := range paper.Questions {
		for _, gen := range v.GeneratedPool {
			if q.ID == gen.ID || q.Type != gen.Type {
				continue
			}
			sim := jaccardSimilarity(q.Points, gen.Points)
			if sim >= SimilarityThreshold {
				dupPairs = append(dupPairs,
					fmt.Sprintf("%s ~ %s (gen-pool, sim=%.2f)",
						q.ID, gen.ID, sim))
			}
		}
	}

	return dupPairs
}

// --- Helpers ---

func bandFromDifficulty(d float64) models.DifficultyBand {
	return models.BandFromDifficulty(d)
}

func klDivergence(actual, target map[models.DifficultyBand]float64) float64 {
	kl := 0.0
	for band, p := range target {
		q := actual[band]
		if q <= 0 {
			q = 1e-10 // avoid log(0)
		}
		if p > 0 {
			kl += p * math.Log(p/q)
		}
	}
	return kl
}

func jaccardSimilarity(a, b []string) float64 {
	setA := make(map[string]bool)
	for _, s := range a {
		setA[s] = true
	}
	setB := make(map[string]bool)
	for _, s := range b {
		setB[s] = true
	}

	intersection := 0
	for s := range setA {
		if setB[s] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func paperTotalQuestionScore(q models.GeneratedQuestion) float64 {
	// Score is carried through from the spec entry by the generator. Do not
	// invent a score for malformed/legacy data: the structural check should
	// make the paper fail instead of allowing a zero-value question to pass.
	return q.Score
}

func paperStructureIssues(paper models.ExamPaper) []string {
	issues := make([]string, 0)
	if len(paper.Questions) == 0 {
		issues = append(issues, "试卷没有题目")
	}
	seen := make(map[string]struct{}, len(paper.Questions))
	questionScore := 0.0
	for i, q := range paper.Questions {
		prefix := fmt.Sprintf("第%d题", i+1)
		if strings.TrimSpace(q.ID) == "" {
			issues = append(issues, prefix+"缺少题目 ID")
		} else if _, exists := seen[q.ID]; exists {
			issues = append(issues, prefix+"题目 ID 重复: "+q.ID)
		} else {
			seen[q.ID] = struct{}{}
		}
		if q.Score <= 0 || math.IsNaN(q.Score) || math.IsInf(q.Score, 0) {
			issues = append(issues, prefix+"分值必须是正数")
		} else {
			questionScore += q.Score
		}
		if math.IsNaN(q.Difficulty) || math.IsInf(q.Difficulty, 0) || q.Difficulty < 0 || q.Difficulty > 1 {
			issues = append(issues, prefix+"难度必须位于[0,1]")
		}
		switch q.Type {
		case models.TypeChoice, models.TypeMultiChoice, models.TypeFillBlank, models.TypeMajor:
		default:
			issues = append(issues, prefix+"题型无效: "+string(q.Type))
		}
		if q.Type == models.TypeChoice || q.Type == models.TypeMultiChoice {
			if issue := choice.Validate(choice.Parse(q.Answer), q.Type == models.TypeMultiChoice); issue != "" {
				issues = append(issues, prefix+"标准答案选项无效")
			}
		}
	}
	if len(paper.Questions) > 0 && !finiteInRange(paper.TotalScore, questionScore-0.01, questionScore+0.01) {
		issues = append(issues, "试卷总分与题目分值之和不一致")
	}
	if len(paper.SpecTable.Entries) > 0 {
		seenSpecs := make(map[string]struct{}, len(paper.SpecTable.Entries))
		specByID := make(map[string]models.SpecEntry, len(paper.SpecTable.Entries))
		specTotal := 0.0
		for _, entry := range paper.SpecTable.Entries {
			id := strings.TrimSpace(entry.ID)
			if id == "" {
				issues = append(issues, "细目表包含空题位 ID")
				continue
			}
			if _, exists := seenSpecs[id]; exists {
				issues = append(issues, "细目表题位 ID 重复: "+id)
			}
			seenSpecs[id] = struct{}{}
			specByID[id] = entry
			if entry.Score <= 0 || math.IsNaN(entry.Score) || math.IsInf(entry.Score, 0) {
				issues = append(issues, "细目表题位 "+id+" 分值无效")
			} else {
				specTotal += entry.Score
			}
		}
		if !finiteInRange(paper.SpecTable.TotalScore, specTotal-0.01, specTotal+0.01) {
			issues = append(issues, "细目表总分与题位分值之和不一致")
		}
		usedSpecs := make(map[string]struct{}, len(paper.Questions))
		for i, q := range paper.Questions {
			if strings.TrimSpace(q.SpecID) == "" {
				issues = append(issues, fmt.Sprintf("第%d题缺少细目表题位 ID", i+1))
			} else if _, exists := seenSpecs[q.SpecID]; !exists {
				issues = append(issues, fmt.Sprintf("第%d题引用未知细目表题位 %s", i+1, q.SpecID))
			} else {
				if _, duplicate := usedSpecs[q.SpecID]; duplicate {
					issues = append(issues, fmt.Sprintf("细目表题位 %s 被多道题引用", q.SpecID))
				}
				usedSpecs[q.SpecID] = struct{}{}
				entry := specByID[q.SpecID]
				if q.Type != entry.Type || math.Abs(q.Score-entry.Score) > 0.01 {
					issues = append(issues, fmt.Sprintf("第%d题与细目表题位 %s 的题型或分值不一致", i+1, q.SpecID))
				}
			}
		}
		if len(usedSpecs) != len(seenSpecs) {
			issues = append(issues, "生成题目数量与细目表题位数量不一致")
		}
	}
	if paper.TotalScore < 0 || math.IsNaN(paper.TotalScore) || math.IsInf(paper.TotalScore, 0) {
		issues = append(issues, "试卷总分无效")
	}
	return issues
}

func paperContentIssues(paper models.ExamPaper) []string {
	issues := make([]string, 0)
	if len(paper.Questions) == 0 {
		return append(issues, "试卷没有可交付题目")
	}
	if len(paper.SpecTable.Entries) == 0 {
		issues = append(issues, "试卷缺少生成细目表")
	}
	for i, q := range paper.Questions {
		label := fmt.Sprintf("第%d题", i+1)
		if q.Status != "ready" {
			issues = append(issues, label+"状态不是 ready")
		}
		if blankOrPlaceholder(q.Stem) {
			issues = append(issues, label+"题面为空或为占位内容")
		}
		if blankOrPlaceholder(q.Answer) {
			issues = append(issues, label+"参考答案为空或为占位内容")
		}
		for _, issue := range quality.Issues(q) {
			issues = append(issues, label+issue)
		}
		for _, issue := range quality.SubjectContentIssues(paper.Subject, q.Points, questionContent(q)) {
			issues = append(issues, label+issue)
		}
		if q.Type == models.TypeChoice || q.Type == models.TypeMultiChoice {
			if len(q.Options) != 4 {
				issues = append(issues, fmt.Sprintf("%s选项数量为%d", label, len(q.Options)))
			} else {
				seenOptions := make(map[string]struct{}, len(q.Options))
				for option, value := range q.Options {
					key := strings.TrimSpace(value)
					if blankOrPlaceholder(value) {
						issues = append(issues, fmt.Sprintf("%s第%d个选项为空或为占位内容", label, option+1))
					}
					if _, exists := seenOptions[key]; key != "" && exists {
						issues = append(issues, label+"包含重复选项")
					}
					seenOptions[key] = struct{}{}
				}
			}
		}
	}
	rubrics := make(map[string]models.Rubric, len(paper.Rubrics))
	questionIDs := make(map[string]struct{}, len(paper.Questions))
	for _, q := range paper.Questions {
		questionIDs[q.ID] = struct{}{}
	}
	for _, rubric := range paper.Rubrics {
		id := strings.TrimSpace(rubric.QuestionID)
		if id == "" {
			issues = append(issues, "评分标准缺少题目 ID")
			continue
		}
		if _, exists := rubrics[id]; exists {
			issues = append(issues, "评分标准重复定义: "+id)
			continue
		}
		rubrics[id] = rubric
		if _, exists := questionIDs[id]; !exists {
			issues = append(issues, "评分标准引用未知题目: "+id)
		}
		if len(rubric.Items) == 0 {
			issues = append(issues, "题目 "+id+" 的评分标准为空")
			continue
		}
		seenItems := make(map[string]struct{}, len(rubric.Items))
		total := 0.0
		for _, item := range rubric.Items {
			itemID := strings.TrimSpace(item.ID)
			if itemID == "" {
				issues = append(issues, "题目 "+id+" 的评分标准包含空检查点 ID")
			}
			if _, exists := seenItems[itemID]; exists && itemID != "" {
				issues = append(issues, "题目 "+id+" 的评分标准包含重复检查点: "+itemID)
			}
			seenItems[itemID] = struct{}{}
			if item.MaxScore <= 0 || math.IsNaN(item.MaxScore) || math.IsInf(item.MaxScore, 0) {
				issues = append(issues, "题目 "+id+" 的评分标准包含无效分值")
			} else {
				total += item.MaxScore
			}
		}
		for _, q := range paper.Questions {
			if q.ID == id && (math.IsNaN(total) || math.IsInf(total, 0) || math.Abs(total-q.Score) > 0.01) {
				issues = append(issues, fmt.Sprintf("题目 %s 的评分标准总分 %.2f 与题目分值 %.2f 不一致", id, total, q.Score))
			}
		}
	}
	for _, q := range paper.Questions {
		if _, exists := rubrics[q.ID]; !exists {
			issues = append(issues, "题目 "+q.ID+" 缺少评分标准")
		}
	}
	return issues
}

func questionContent(q models.GeneratedQuestion) string {
	return strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n")
}

func blankOrPlaceholder(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || strings.HasPrefix(trimmed, "%") || strings.Contains(strings.ToUpper(trimmed), "TODO")
}

// stemUsable reports whether a stem carries real content. Placeholder stems
// (offline generator fallback) start with a LaTeX comment or contain TODO —
// they must not participate in text-similarity dedup.
func stemUsable(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || strings.HasPrefix(t, "%") {
		return false
	}
	return !strings.Contains(t, "TODO")
}

// stemSimilarity compares two stems via character-bigram Jaccard over
// letters/digits only (LaTeX markup and whitespace stripped). Stems shorter
// than 8 significant characters are treated as incomparable.
func stemSimilarity(a, b string) float64 {
	ba, bb := stemBigrams(a), stemBigrams(b)
	if len(ba) == 0 || len(bb) == 0 {
		return 0
	}
	inter := 0
	for g := range ba {
		if bb[g] {
			inter++
		}
	}
	union := len(ba) + len(bb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func stemBigrams(s string) map[string]bool {
	var runes []rune
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			runes = append(runes, r)
		}
	}
	if len(runes) < 8 {
		return nil
	}
	out := make(map[string]bool, len(runes))
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i:i+2])] = true
	}
	return out
}
