package main

// End-to-end orchestration on top of the generation stages:
//   - runPipeline: builds a blueprint, generates a paper, validates, and assembles it
//   - buildPaper: glues stage outputs into the final ExamPaper
//   - repairPaper: Stage-6 repair protocol (regenerate only violated题位,
//     ≤maxRounds, then degrade to last state + final validation report)

import (
	"context"
	"fmt"
	"math"
	"time"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/assembler"
	"GoT0GenPaper/internal/pipeline/orchestrator"
	"GoT0GenPaper/internal/pipeline/parser"
	"GoT0GenPaper/internal/pipeline/quality"
	"GoT0GenPaper/internal/pipeline/validator"
)

// PipelineRequest is the end-to-end run configuration.
type PipelineRequest struct {
	Source          string                 `json:"source"`          // 真题文件/目录(空=使用科目默认目录)
	Blueprint       orchestrator.Blueprint `json:"blueprint"`       // 由选定科目的配置构建
	MaxRepairRounds int                    `json:"maxRepairRounds"` // 建议≤3
}

// PipelineSummary reports the end-to-end run outcome.
type PipelineSummary struct {
	ParseCount   int                         `json:"parseCount"`
	SpecCount    int                         `json:"specCount"`
	Generated    int                         `json:"generated"`
	RepairRounds int                         `json:"repairRounds"`
	Validation   *validator.ValidationResult `json:"validation"`
	Paper        *models.ExamPaper           `json:"paper"`
	Output       *assembler.AssembleOutput   `json:"output"`
	Warnings     []string                    `json:"warnings"`
}

// runPipeline executes the paper-generation stages in sequence.
//
//	[1] parse (skipped when Source is empty and stored data exists)
//	[2] GNN weights (wired into CP-SAT)
//	[3] compose spec table
//	[4] generate questions
//	[5] validate + repair loop
//	[6] assemble + persist the exam LaTeX and paper metadata
func (a *app) runPipeline(req PipelineRequest) (*PipelineSummary, error) {
	summary := &PipelineSummary{Warnings: []string{}}
	if _, err := blueprintForSubject(req.Blueprint.Subject); err != nil {
		return nil, fmt.Errorf("subject profile: %w", err)
	}

	useCachedParse := req.Source == ""
	// The subject profile owns the source directory when the caller did not
	// provide an explicit source. This keeps the selected subject meaningful
	// and avoids accidentally reusing another subject's parsed questions.
	if req.Source == "" {
		source, err := a.sourceForSubject(req.Blueprint.Subject)
		if err != nil {
			return nil, fmt.Errorf("subject source: %w", err)
		}
		req.Source = source
	}

	// Stage 1: parse. The subject-scoped cache is reused for the normal CLI
	// path; an explicit --source always means the caller requested re-parse.
	if useCachedParse && a.loadStoredParse() == nil && cachedParseMatchesSubject(a.lastParse, req.Blueprint.Subject) {
		// Reuse the already annotated corpus; this avoids 431 repeat LLM calls
		// every time a paper is generated from the same subject.
	} else if req.Source != "" {
		if _, err := a.parseExams(req.Source); err != nil {
			return nil, fmt.Errorf("stage1 parse: %w", err)
		}
	} else if a.lastParse == nil {
		if err := a.loadStoredParse(); err != nil {
			return nil, fmt.Errorf("stage1 parse: no source given and no stored parse result: %w", err)
		}
	}
	if a.lastParse == nil || len(a.lastParse.Questions) == 0 {
		return nil, fmt.Errorf("stage1 parse: no past questions available")
	}
	questions := a.lastParse.Questions
	if subject, ok := subjectProfile(req.Blueprint.Subject); ok && (subject.Key == "math2" || subject.Key == "politics") {
		questions = quality.FilterPastQuestions(subject.Key, questions)
		a.lastParse.Questions = append([]models.PastQuestion(nil), questions...)
		if err := a.attachParseArtifacts(a.lastParse); err != nil {
			return nil, fmt.Errorf("stage1 subject filter: %w", err)
		}
	}
	summary.ParseCount = len(questions)

	// Stage 2: GNN weights.
	if _, err := a.predictWeights(questions); err != nil {
		return nil, fmt.Errorf("stage2 gnn: %w", err)
	}

	// Stage 3: compose spec table.
	spec, err := a.composeExam(req.Blueprint, questions)
	if err != nil {
		return nil, fmt.Errorf("stage3 compose: %w", err)
	}
	summary.SpecCount = len(spec.Entries)
	// Undershoot guard: a spec table short of the blueprint total would
	// otherwise propagate silently into the assembled paper.
	if req.Blueprint.TotalScore > 0 && math.Abs(spec.TotalScore-req.Blueprint.TotalScore) > 0.5 {
		summary.Warnings = append(summary.Warnings, fmt.Sprintf(
			"CP-SAT 未能精确命中蓝图总分 (spec=%.1f, 蓝图=%.1f)", spec.TotalScore, req.Blueprint.TotalScore))
	}

	// Stage 4: generate questions.
	qs, err := a.generateQuestions(*spec)
	if err != nil {
		return nil, fmt.Errorf("stage4 generate: %w", err)
	}
	summary.Generated = len(qs)

	for _, q := range qs {
		if q.Status == "needs_review" {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("题目 %s 生成结果需要人工复核", q.ID))
		}
	}
	// Assemble the generated question set as the paper model.
	paper := buildPaper(*spec, qs)
	paper.ID = fmt.Sprintf("paper_%d", time.Now().UnixMilli())

	// Stage 6: validate + repair loop.
	maxRounds := req.MaxRepairRounds
	if maxRounds < 0 {
		maxRounds = 0
	}
	paper, val, rounds, err := a.repairPaper(a.ctx, paper, a.lastBlueprint, maxRounds)
	if err != nil {
		return nil, fmt.Errorf("stage6 validate: %w", err)
	}
	summary.Validation = val
	summary.RepairRounds = rounds
	if !val.StructureOK || !val.ContentReady || !val.CoverageOK || !val.DifficultyOK || !val.NoDuplicates || !val.ScoreTotalOK {
		summary.Warnings = append(summary.Warnings,
			"整卷校验未完全通过(repair后仍存在违例), 详见 validation 字段")
	}

	// Stage 7: assemble + persist.
	out, err := a.assemblePaper(paper)
	if err != nil {
		return nil, fmt.Errorf("stage7 assemble: %w", err)
	}
	summary.Paper = &paper
	summary.Output = out
	return summary, nil
}

func cachedParseMatchesSubject(parsed *parser.ParseResult, subject string) bool {
	profile, ok := subjectProfile(subject)
	if !ok || parsed == nil || len(parsed.Questions) == 0 {
		return false
	}
	for _, question := range parsed.Questions {
		if question.Subject != profile.Key && question.Source != profile.Key {
			return false
		}
		if profile.Key == "politics" && !quality.IsPoliticsPointSet(question.Points) {
			return false
		}
	}
	return true
}

// buildPaper glues stage outputs into the final ExamPaper model.
func buildPaper(spec models.SpecTable, qs []models.GeneratedQuestion) models.ExamPaper {
	paper := models.ExamPaper{
		Subject:   spec.Subject,
		Questions: append([]models.GeneratedQuestion(nil), qs...),
		SpecTable: spec,
	}
	total := 0.0
	for _, q := range qs {
		total += q.Score
	}
	paper.TotalScore = total
	return paper
}

// repairPaper runs the validation loop and regenerates only violated question slots.
// After maxRounds it degrades: the paper is returned as-is with the final
// validation result so the caller (or user) can decide.
func (a *app) repairPaper(ctx context.Context, paper models.ExamPaper, bp *orchestrator.Blueprint, maxRounds int) (models.ExamPaper, *validator.ValidationResult, int, error) {
	var lastVal *validator.ValidationResult
	for round := 0; round < maxRounds; round++ {
		val, err := a.validator.Validate(ctx, paper, bp)
		if err != nil {
			return paper, lastVal, round, err
		}
		lastVal = val
		if val.StructureOK && val.ContentReady && val.CoverageOK && val.DifficultyOK && val.NoDuplicates && val.ScoreTotalOK {
			return paper, val, round, nil
		}

		// Re-generate only the violated spec slots.
		repaired := a.regenerateSlots(ctx, &paper, val.ViolatedSpecIDs)
		if repaired {
			paper.Questions = orderQuestionsBySpec(paper.Questions, paper.SpecTable.Entries)
			paper.TotalScore = sumPaperQuestionScores(paper.Questions)
		}
		if !repaired {
			// Nothing could be regenerated — degrade with last validation.
			return paper, lastVal, round + 1, nil
		}
	}

	// Final validation after exhausting repair rounds.
	val, err := a.validator.Validate(ctx, paper, bp)
	if err != nil {
		return paper, lastVal, maxRounds, err
	}
	return paper, val, maxRounds, nil
}

// regenerateSlots regenerates only the requested entries; all other questions remain unchanged.
func (a *app) regenerateSlots(ctx context.Context, paper *models.ExamPaper, specIDs []string) bool {
	if paper == nil || len(specIDs) == 0 {
		return false
	}
	specByID := make(map[string]models.SpecEntry, len(paper.SpecTable.Entries))
	for _, entry := range paper.SpecTable.Entries {
		specByID[entry.ID] = entry
	}
	repaired := false
	seen := make(map[string]bool, len(specIDs))
	for _, sid := range specIDs {
		if seen[sid] {
			continue
		}
		seen[sid] = true
		entry, ok := specByID[sid]
		if !ok {
			continue
		}
		nq, err := a.gen.GenerateForSubject(ctx, paper.Subject, entry)
		if err != nil {
			continue
		}
		if replaceQuestionForSpec(paper, sid, nq) {
			repaired = true
		}
	}
	return repaired
}

// replaceQuestionForSpec replaces a generated question in place, or appends a
// new question when validation reported a missing spec slot. The latter is
// important for coverage repair: a missing slot cannot be repaired by a
// replacement-only algorithm.
func replaceQuestionForSpec(paper *models.ExamPaper, specID string, question models.GeneratedQuestion) bool {
	for i := range paper.Questions {
		if paper.Questions[i].SpecID == specID {
			paper.Questions[i] = question
			return true
		}
	}
	paper.Questions = append(paper.Questions, question)
	return true
}

func orderQuestionsBySpec(questions []models.GeneratedQuestion, entries []models.SpecEntry) []models.GeneratedQuestion {
	bySpec := make(map[string][]models.GeneratedQuestion, len(entries))
	knownSpecs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		knownSpecs[entry.ID] = struct{}{}
	}
	for _, q := range questions {
		bySpec[q.SpecID] = append(bySpec[q.SpecID], q)
	}
	ordered := make([]models.GeneratedQuestion, 0, len(questions))
	for _, entry := range entries {
		ordered = append(ordered, bySpec[entry.ID]...)
	}
	// Preserve unknown/empty references at the end so validation reports them
	// instead of silently dropping user data during repair.
	for _, q := range questions {
		if _, known := knownSpecs[q.SpecID]; !known {
			ordered = append(ordered, q)
		}
	}
	return ordered
}

func sumPaperQuestionScores(questions []models.GeneratedQuestion) float64 {
	total := 0.0
	for _, q := range questions {
		if !math.IsNaN(q.Score) && !math.IsInf(q.Score, 0) {
			total += q.Score
		}
	}
	return total
}
