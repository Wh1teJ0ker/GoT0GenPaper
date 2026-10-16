package main

import (
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

// TestAllSubjectsOfflinePipelineEndToEnd drives every selectable subject
// through the whole pipeline (parse → compose → generate → validate/repair →
// assemble) against the bundled corpus with an unreachable LLM endpoint. It is
// the regression net for composition usability: it fails when a subject's
// blueprint can no longer be filled from its historical corpus, when the
// generation gate rejects composed spec slots, or when offline generation
// cannot degrade to placeholders.
func TestAllSubjectsOfflinePipelineEndToEnd(t *testing.T) {
	if os.Getenv("RUN_CORPUS_TESTS") != "1" {
		t.Skip("set RUN_CORPUS_TESTS=1 to run the offline pipeline over all bundled corpora")
	}
	for _, key := range []string{"408", "math1", "math2", "english1", "english2", "politics"} {
		t.Run(key, func(t *testing.T) {
			a := newTestApp(t)
			bp, err := blueprintForSubject(key)
			if err != nil {
				t.Fatal(err)
			}
			profile, _ := subjectProfile(key)
			summary, err := a.runPipeline(PipelineRequest{
				Source:          repoPath("data", "exams", "csgraduates", profile.Source[len("exams/csgraduates/"):]),
				Blueprint:       bp,
				MaxRepairRounds: 1,
			})
			if err != nil {
				t.Fatalf("runPipeline %s: %v", key, err)
			}
			if summary.SpecCount != profile.NumQuestions {
				t.Errorf("%s composed %d spec entries, want %d", key, summary.SpecCount, profile.NumQuestions)
			}
			if summary.Generated != summary.SpecCount {
				t.Errorf("%s generated %d questions for %d spec entries", key, summary.Generated, summary.SpecCount)
			}
			if math.Abs(summary.Paper.TotalScore-profile.TotalScore) > 0.5 {
				t.Errorf("%s paper total = %.1f, want %.1f", key, summary.Paper.TotalScore, profile.TotalScore)
			}
			// Composition must label every slot with a real syllabus node: 408
			// slots need one of the four courses, math slots need a discipline
			// (never a bare chapter), and no subject may leak the generic
			// "_auto_" marker into a spec table.
			for _, entry := range summary.Paper.SpecTable.Entries {
				for _, point := range entry.Points {
					if strings.Contains(point, "_auto_") {
						t.Errorf("%s slot %s kept a synthetic knowledge point: %v", key, entry.ID, entry.Points)
						break
					}
				}
				if key == "408" && !models.Is408Discipline(models.DisciplineFromPoints(entry.Points)) {
					t.Errorf("408 slot %s has no course label: %v", entry.ID, entry.Points)
				}
				if (key == "math1" || key == "math2") && !isMathDiscipline(entry.Points[0]) {
					t.Errorf("%s slot %s does not start at a discipline: %v", key, entry.ID, entry.Points)
				}
			}
			// The difficulty target is part of the blueprint contract. The
			// bundled corpus carries no difficulty labels, so this also guards the
			// corpus-relative difficulty estimate in the parser.
			if summary.Validation == nil || !summary.Validation.DifficultyOK {
				t.Errorf("%s difficulty target not met: %+v", key, summary.Validation)
			}
			if key == "408" {
				assert408OfficialSplit(t, summary.Paper.SpecTable.Entries)
			}
			t.Logf("%s: parse=%d spec=%d generated=%d total=%.0f (want %.0f)",
				key, summary.ParseCount, summary.SpecCount, summary.Generated, summary.Paper.TotalScore, profile.TotalScore)
		})
	}
}

// assert408OfficialSplit pins the official 2026 408 structure: 45/45/35/25
// points per course, 11/11/10/8 single-choice questions and 2/2/2/1 composite
// questions, with the published per-question composite scores (13+10, 10+13,
// 7+8, 9) restored through the blueprint's score patterns.
func assert408OfficialSplit(t *testing.T, entries []models.SpecEntry) {
	t.Helper()
	wantScore := map[string]map[models.QuestionType]float64{
		"数据结构":  {models.TypeChoice: 22, models.TypeMajor: 23},
		"组成原理":  {models.TypeChoice: 22, models.TypeMajor: 23},
		"操作系统":  {models.TypeChoice: 20, models.TypeMajor: 15},
		"计算机网络": {models.TypeChoice: 16, models.TypeMajor: 9},
	}
	wantCount := map[string]map[models.QuestionType]int{
		"数据结构":  {models.TypeChoice: 11, models.TypeMajor: 2},
		"组成原理":  {models.TypeChoice: 11, models.TypeMajor: 2},
		"操作系统":  {models.TypeChoice: 10, models.TypeMajor: 2},
		"计算机网络": {models.TypeChoice: 8, models.TypeMajor: 1},
	}
	scores := make(map[string]map[models.QuestionType]float64)
	counts := make(map[string]map[models.QuestionType]int)
	majorScores := make(map[string][]float64)
	for _, entry := range entries {
		discipline := models.DisciplineFromPoints(entry.Points)
		if scores[discipline] == nil {
			scores[discipline] = make(map[models.QuestionType]float64)
			counts[discipline] = make(map[models.QuestionType]int)
		}
		scores[discipline][entry.Type] += entry.Score
		counts[discipline][entry.Type]++
		if entry.Type == models.TypeMajor {
			majorScores[discipline] = append(majorScores[discipline], entry.Score)
		}
	}
	for discipline, quotas := range wantScore {
		for questionType, want := range quotas {
			if scores[discipline][questionType] != want {
				t.Errorf("408 %s %s = %.0f 分, want %.0f", discipline, questionType, scores[discipline][questionType], want)
			}
		}
	}
	for discipline, quotas := range wantCount {
		for questionType, want := range quotas {
			if counts[discipline][questionType] != want {
				t.Errorf("408 %s %s = %d 题, want %d", discipline, questionType, counts[discipline][questionType], want)
			}
		}
	}
	wantMajors := map[string][]float64{
		"数据结构":  {13, 10},
		"组成原理":  {10, 13},
		"操作系统":  {7, 8},
		"计算机网络": {9},
	}
	for discipline, want := range wantMajors {
		got := append([]float64(nil), majorScores[discipline]...)
		sort.Float64s(got)
		want = append([]float64(nil), want...)
		sort.Float64s(want)
		if len(got) != len(want) {
			t.Errorf("408 %s composite scores = %v, want %v", discipline, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("408 %s composite scores = %v, want %v", discipline, got, want)
				break
			}
		}
	}
}

// isMathDiscipline reports whether a knowledge point starts at a math
// discipline rather than a chapter name.
func isMathDiscipline(value string) bool {
	switch value {
	case "高等数学", "线性代数", "概率论与数理统计":
		return true
	default:
		return false
	}
}
