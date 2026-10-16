package main

import (
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
)

func TestSelectableSubjectProfiles(t *testing.T) {
	want := map[string]struct {
		label       string
		total       float64
		questions   int
		files       int
		parsed      int
		quota       map[models.QuestionType]float64
		countQuota  map[models.QuestionType]int
		discQuota   map[string]map[models.QuestionType]float64
		discCounts  map[string]map[models.QuestionType]int
		discPattern map[string]map[models.QuestionType][]float64
	}{
		"408": {label: "408", total: 150, questions: 47, files: 18, parsed: 846,
			quota:      map[models.QuestionType]float64{models.TypeChoice: 80, models.TypeMajor: 70},
			countQuota: map[models.QuestionType]int{models.TypeChoice: 40, models.TypeMajor: 7},
			discQuota: map[string]map[models.QuestionType]float64{
				"数据结构":  {models.TypeChoice: 22, models.TypeMajor: 23},
				"组成原理":  {models.TypeChoice: 22, models.TypeMajor: 23},
				"操作系统":  {models.TypeChoice: 20, models.TypeMajor: 15},
				"计算机网络": {models.TypeChoice: 16, models.TypeMajor: 9},
			},
			discCounts: map[string]map[models.QuestionType]int{
				"数据结构":  {models.TypeChoice: 11, models.TypeMajor: 2},
				"组成原理":  {models.TypeChoice: 11, models.TypeMajor: 2},
				"操作系统":  {models.TypeChoice: 10, models.TypeMajor: 2},
				"计算机网络": {models.TypeChoice: 8, models.TypeMajor: 1},
			},
			discPattern: map[string]map[models.QuestionType][]float64{
				"数据结构":  {models.TypeMajor: {13, 10}},
				"组成原理":  {models.TypeMajor: {10, 13}},
				"操作系统":  {models.TypeMajor: {7, 8}},
				"计算机网络": {models.TypeMajor: {9}},
			}},
		"math1": {label: "数学一", total: 150, questions: 22, files: 19, parsed: 431, quota: map[models.QuestionType]float64{models.TypeChoice: 50, models.TypeFillBlank: 30, models.TypeMajor: 70}, countQuota: map[models.QuestionType]int{models.TypeChoice: 10, models.TypeFillBlank: 6, models.TypeMajor: 6}},
		"math2": {label: "数学二", total: 150, questions: 22, files: 19, parsed: 431,
			quota:      map[models.QuestionType]float64{models.TypeChoice: 50, models.TypeFillBlank: 30, models.TypeMajor: 70},
			countQuota: map[models.QuestionType]int{models.TypeChoice: 10, models.TypeFillBlank: 6, models.TypeMajor: 6},
			discQuota: map[string]map[models.QuestionType]float64{
				"高等数学": {models.TypeChoice: 35, models.TypeFillBlank: 25, models.TypeMajor: 58},
				"线性代数": {models.TypeChoice: 15, models.TypeFillBlank: 5, models.TypeMajor: 12},
			},
			discCounts: map[string]map[models.QuestionType]int{
				"高等数学": {models.TypeChoice: 7, models.TypeFillBlank: 5, models.TypeMajor: 5},
				"线性代数": {models.TypeChoice: 3, models.TypeFillBlank: 1, models.TypeMajor: 1},
			}},
		"english1": {label: "英语一", total: 100, questions: 52, files: 17, parsed: 880, quota: map[models.QuestionType]float64{models.TypeChoice: 60, models.TypeMajor: 40}, countQuota: map[models.QuestionType]int{models.TypeChoice: 45, models.TypeMajor: 7}},
		"english2": {label: "英语二", total: 100, questions: 48, files: 17, parsed: 816, quota: map[models.QuestionType]float64{models.TypeChoice: 60, models.TypeMajor: 40}, countQuota: map[models.QuestionType]int{models.TypeChoice: 45, models.TypeMajor: 3}},
		"politics": {label: "政治", total: 100, questions: 38, files: 17, parsed: 646, quota: map[models.QuestionType]float64{models.TypeChoice: 16, models.TypeMultiChoice: 34, models.TypeMajor: 50}, countQuota: map[models.QuestionType]int{models.TypeChoice: 16, models.TypeMultiChoice: 17, models.TypeMajor: 5}},
	}

	for key, expected := range want {
		profile, ok := subjectProfile(key)
		if !ok {
			t.Fatalf("subjectProfile(%q) not found", key)
		}
		if profile.Label != expected.label || profile.TotalScore != expected.total || profile.NumQuestions != expected.questions {
			t.Errorf("%s profile = label %q, score %.0f, questions %d; want %q, %.0f, %d", key, profile.Label, profile.TotalScore, profile.NumQuestions, expected.label, expected.total, expected.questions)
		}
		if profile.ExamFiles != expected.files || profile.ParsedQuestions != expected.parsed {
			t.Errorf("%s data = %d files/%d questions; want %d/%d", key, profile.ExamFiles, profile.ParsedQuestions, expected.files, expected.parsed)
		}
		if profile.SyllabusVerified {
			t.Errorf("%s is marked syllabus verified without an official outline", key)
		}
		if profile.SyllabusStatus == "" || profile.SyllabusNote == "" {
			t.Errorf("%s is missing syllabus crawl status or note", key)
		}
		for qt, quota := range expected.quota {
			if profile.TypeQuota[qt] != quota {
				t.Errorf("%s quota[%s] = %.0f; want %.0f", key, qt, profile.TypeQuota[qt], quota)
			}
		}
		for qt, count := range expected.countQuota {
			if profile.TypeCountQuota[qt] != count {
				t.Errorf("%s count quota[%s] = %d; want %d", key, qt, profile.TypeCountQuota[qt], count)
			}
		}
		if len(profile.DisciplineQuota) != len(expected.discQuota) || len(profile.DisciplineCountQuota) != len(expected.discCounts) {
			t.Errorf("%s discipline quotas = %d score groups/%d count groups; want %d/%d", key, len(profile.DisciplineQuota), len(profile.DisciplineCountQuota), len(expected.discQuota), len(expected.discCounts))
		}
		for discipline, quotas := range expected.discQuota {
			for qt, score := range quotas {
				if profile.DisciplineQuota[discipline][qt] != score {
					t.Errorf("%s discipline quota[%s][%s] = %.0f; want %.0f", key, discipline, qt, profile.DisciplineQuota[discipline][qt], score)
				}
			}
		}
		if len(profile.DisciplineScorePattern) != len(expected.discPattern) {
			t.Errorf("%s discipline score patterns = %d; want %d", key, len(profile.DisciplineScorePattern), len(expected.discPattern))
		}
		for discipline, patterns := range expected.discPattern {
			for qt, pattern := range patterns {
				got := profile.DisciplineScorePattern[discipline][qt]
				if !reflect.DeepEqual(got, pattern) {
					t.Errorf("%s discipline score pattern[%s][%s] = %v; want %v", key, discipline, qt, got, pattern)
				}
			}
		}
		for discipline, quotas := range expected.discCounts {
			for qt, count := range quotas {
				if profile.DisciplineCountQuota[discipline][qt] != count {
					t.Errorf("%s discipline count quota[%s][%s] = %d; want %d", key, discipline, qt, profile.DisciplineCountQuota[discipline][qt], count)
				}
			}
		}
		bp, err := blueprintForSubject(key)
		if err != nil {
			t.Fatalf("blueprintForSubject(%q): %v", key, err)
		}
		if bp.Subject != expected.label || math.Abs(bp.TotalScore-expected.total) > 0.001 || bp.NumQuestions != expected.questions {
			t.Errorf("%s blueprint does not match profile: %+v", key, bp)
		}
		for qt, count := range expected.countQuota {
			if bp.TypeCountQuota[qt] != count {
				t.Errorf("%s blueprint count quota[%s] = %d; want %d", key, qt, bp.TypeCountQuota[qt], count)
			}
		}
		for discipline, quotas := range expected.discQuota {
			for qt, score := range quotas {
				if bp.DisciplineQuota[discipline][qt] != score {
					t.Errorf("%s blueprint discipline quota[%s][%s] = %.0f; want %.0f", key, discipline, qt, bp.DisciplineQuota[discipline][qt], score)
				}
			}
		}
		for discipline, quotas := range expected.discCounts {
			for qt, count := range quotas {
				if bp.DisciplineCountQuota[discipline][qt] != count {
					t.Errorf("%s blueprint discipline count quota[%s][%s] = %d; want %d", key, discipline, qt, bp.DisciplineCountQuota[discipline][qt], count)
				}
			}
		}
	}
}

func TestSubjectSourcesMatchBundledData(t *testing.T) {
	for _, key := range []string{"408", "math1", "math2", "english1", "english2", "politics"} {
		t.Logf("compose subject %s", key)
		profile, _ := subjectProfile(key)
		path := repoPath("data", profile.Source)
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatalf("%s source %q: %v", key, path, err)
		}
		if len(entries) == 0 {
			t.Errorf("%s source %q is empty", key, path)
		}
	}
}

func TestBlueprintForSubjectRejectsUnknownSubject(t *testing.T) {
	if _, err := blueprintForSubject("math3"); err == nil {
		t.Fatal("blueprintForSubject(math3) accepted an unselectable subject")
	}
}

func TestDifficultyPresetsAndHistogramOverrides(t *testing.T) {
	tests := []struct {
		name   string
		preset string
		raw    string
		want   map[models.DifficultyBand]float64
	}{
		{name: "subject default", want: map[models.DifficultyBand]float64{models.BandEasy: 0.1, models.BandMedium: 0.45, models.BandHard: 0.45}},
		{name: "easy", preset: "easy", want: map[models.DifficultyBand]float64{models.BandEasy: 0.5, models.BandMedium: 0.4, models.BandHard: 0.1}},
		{name: "medium", preset: "medium", want: map[models.DifficultyBand]float64{models.BandEasy: 0.2, models.BandMedium: 0.6, models.BandHard: 0.2}},
		{name: "hard", preset: "hard", want: map[models.DifficultyBand]float64{models.BandEasy: 0.1, models.BandMedium: 0.45, models.BandHard: 0.45}},
		{name: "numeric", raw: "0.2,0.6,0.2", want: map[models.DifficultyBand]float64{models.BandEasy: 0.2, models.BandMedium: 0.6, models.BandHard: 0.2}},
		{name: "named", raw: "hard=0.2,easy=0.3,medium=0.5", want: map[models.DifficultyBand]float64{models.BandEasy: 0.3, models.BandMedium: 0.5, models.BandHard: 0.2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bp, err := blueprintForSubjectConfigured("数学二", tt.preset, tt.raw)
			if err != nil {
				t.Fatalf("blueprintForSubjectConfigured: %v", err)
			}
			for band, want := range tt.want {
				if math.Abs(bp.DiffHist[band]-want) > 1e-9 {
					t.Errorf("DiffHist[%d] = %.3f, want %.3f", band, bp.DiffHist[band], want)
				}
			}
		})
	}
}

func TestDifficultyHistogramValidation(t *testing.T) {
	for _, raw := range []string{"0.2,0.2,0.2", "0.2,0.8", "easy=0.2,medium=0.8,hard=0.1", "unknown=1,medium=0,hard=0"} {
		if _, err := parseDifficultyHistogram(raw); err == nil {
			t.Errorf("parseDifficultyHistogram(%q) accepted invalid input", raw)
		}
		if _, err := blueprintForSubjectDifficulty("数学二", "", raw, ""); err == nil {
			t.Errorf("blueprintForSubjectDifficulty accepted invalid histogram %q", raw)
		}
	}
	if _, err := blueprintForSubjectConfigured("数学二", "hard", "0.2,0.6,0.2"); err == nil {
		t.Fatal("difficulty preset and histogram were accepted together")
	}
}

func TestDifficultyStrictnessIsExplicit(t *testing.T) {
	defaultBlueprint, err := blueprintForSubjectDifficulty("数学二", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if defaultBlueprint.DifficultyCountStrict {
		t.Fatal("subject default unexpectedly enabled strict difficulty counts")
	}

	for name, configure := range map[string]func() (orchestrator.Blueprint, error){
		"preset": func() (orchestrator.Blueprint, error) {
			return blueprintForSubjectDifficulty("数学二", "medium", "", "")
		},
		"histogram": func() (orchestrator.Blueprint, error) {
			return blueprintForSubjectDifficulty("数学二", "", "0.2,0.6,0.2", "")
		},
		"counts": func() (orchestrator.Blueprint, error) {
			return blueprintForSubjectDifficulty("数学二", "", "", "2,10,10")
		},
	} {
		bp, err := configure()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bp.DifficultyCountStrict {
			t.Errorf("%s did not enable strict difficulty counts", name)
		}
	}
	politics, err := blueprintForSubjectDifficulty("政治", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !politics.DifficultyCountStrict {
		t.Fatal("politics subject default should enable strict difficulty counts")
	}
	for _, subject := range []string{"英语一", "英语二"} {
		english, err := blueprintForSubjectDifficulty(subject, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if !english.DifficultyCountStrict {
			t.Fatalf("%s subject default should enable strict difficulty counts", subject)
		}
	}
}

func TestDifficultyCountsAreExactAndDerivedFromRatios(t *testing.T) {
	fromRatio, err := blueprintForSubjectDifficulty("数学二", "", "0.1,0.45,0.45", "")
	if err != nil {
		t.Fatalf("ratio blueprint: %v", err)
	}
	want := map[models.DifficultyBand]int{models.BandEasy: 2, models.BandMedium: 10, models.BandHard: 10}
	if !reflect.DeepEqual(fromRatio.DiffCountQuota, want) {
		t.Fatalf("ratio counts = %#v, want %#v", fromRatio.DiffCountQuota, want)
	}

	explicit, err := blueprintForSubjectDifficulty("数学二", "", "", "easy=1,medium=8,hard=13")
	if err != nil {
		t.Fatalf("explicit blueprint: %v", err)
	}
	if !reflect.DeepEqual(explicit.DiffCountQuota, map[models.DifficultyBand]int{
		models.BandEasy: 1, models.BandMedium: 8, models.BandHard: 13,
	}) {
		t.Fatalf("explicit counts = %#v", explicit.DiffCountQuota)
	}
	if math.Abs(explicit.DiffHist[models.BandEasy]-1.0/22.0) > 1e-9 {
		t.Fatalf("explicit histogram was not derived from counts: %#v", explicit.DiffHist)
	}
}

func TestDifficultyCountsValidation(t *testing.T) {
	for _, raw := range []string{"1,2,3", "easy=2,medium=10,hard=9", "easy=-1,medium=10,hard=13", "easy=2,medium=10,hard=10,extra=0"} {
		if _, err := blueprintForSubjectDifficulty("数学二", "", "", raw); err == nil {
			t.Errorf("blueprintForSubjectDifficulty accepted invalid counts %q", raw)
		}
	}
	if _, err := blueprintForSubjectDifficulty("数学二", "medium", "", "2,10,10"); err == nil {
		t.Fatal("difficulty preset and explicit counts were accepted together")
	}
}

func TestMath2CompositionUsesDifficultyTargets(t *testing.T) {
	if os.Getenv("RUN_CORPUS_TESTS") != "1" {
		t.Skip("set RUN_CORPUS_TESTS=1 to compose against the bundled math2 corpus")
	}
	a := newTestApp(t)
	profile, _ := subjectProfile("math2")
	parsed, err := a.parser.Parse(t.Context(), repoPath("data", profile.Source))
	if err != nil {
		t.Fatal(err)
	}
	bp, err := blueprintForSubjectDifficulty("数学二", "", "0.1,0.45,0.45", "")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := a.orch.Compose(t.Context(), &bp, parsed.Questions)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[models.DifficultyBand]int)
	for _, entry := range spec.Entries {
		counts[entry.DiffBand]++
	}
	for band, want := range bp.DiffCountQuota {
		if counts[band] != want {
			t.Errorf("composed difficulty band %d count = %d, want %d (all=%v)", band, counts[band], want, counts)
		}
	}
	if !reflect.DeepEqual(spec.DifficultyTarget, bp.DiffCountQuota) {
		t.Errorf("spec difficulty target = %#v, want %#v", spec.DifficultyTarget, bp.DiffCountQuota)
	}
}

func TestAllSelectableSubjectsCanComposeFromBundledQuestions(t *testing.T) {
	if os.Getenv("RUN_CORPUS_TESTS") != "1" {
		t.Skip("set RUN_CORPUS_TESTS=1 to compose against all bundled corpora")
	}
	a := newTestApp(t)
	for _, key := range []string{"408", "math1", "math2", "english1", "english2", "politics"} {
		profile, _ := subjectProfile(key)
		parsed, err := a.parser.Parse(t.Context(), repoPath("data", profile.Source))
		if err != nil {
			t.Fatalf("parse %s: %v", key, err)
		}
		if len(parsed.Questions) == 0 {
			t.Fatalf("parse %s returned no questions", key)
		}
		bp, _ := blueprintForSubject(key)
		spec, err := a.orch.Compose(t.Context(), &bp, parsed.Questions)
		if err != nil {
			t.Fatalf("compose %s: %v", key, err)
		}
		if spec.Subject != profile.Label || math.Abs(spec.TotalScore-profile.TotalScore) > 0.5 {
			t.Errorf("compose %s = subject %q, score %.1f; want %q, %.1f", key, spec.Subject, spec.TotalScore, profile.Label, profile.TotalScore)
		}
		if len(spec.Entries) == 0 {
			t.Errorf("compose %s returned no spec entries", key)
		}
		counts := make(map[models.QuestionType]int)
		for _, entry := range spec.Entries {
			counts[entry.Type]++
		}
		for questionType, want := range profile.TypeCountQuota {
			if counts[questionType] != want {
				t.Errorf("compose %s type %s count = %d; want %d", key, questionType, counts[questionType], want)
			}
		}
		if bp.DifficultyCountStrict && len(bp.DiffCountQuota) > 0 {
			// Strict subjects (english1/english2/politics) must hit the exact band
			// histogram. The bundled English II corpus labels every item as one
			// band, so this guards the per-band synthetic top-up in composition.
			bands := make(map[models.DifficultyBand]int)
			for _, entry := range spec.Entries {
				bands[entry.DiffBand]++
			}
			for band, want := range bp.DiffCountQuota {
				if bands[band] != want {
					t.Errorf("compose %s band %d count = %d; want %d (all=%v)", key, band, bands[band], want, bands)
				}
			}
		}
		if len(profile.DisciplineQuota) > 0 {
			// Subjects with an official discipline split (408, math2) must hit it
			// exactly, in discipline order, for every question type.
			domainCounts := make(map[string]map[models.QuestionType]int)
			domainScores := make(map[string]map[models.QuestionType]float64)
			lastDomainByType := make(map[models.QuestionType]int)
			seenType := make(map[models.QuestionType]bool)
			for _, entry := range spec.Entries {
				domain := models.DisciplineFromPoints(entry.Points)
				if domainCounts[domain] == nil {
					domainCounts[domain] = make(map[models.QuestionType]int)
					domainScores[domain] = make(map[models.QuestionType]float64)
				}
				domainCounts[domain][entry.Type]++
				domainScores[domain][entry.Type] += entry.Score
				order := disciplineOrderRank(profile.DisciplineOrder, domain)
				if seenType[entry.Type] && order < lastDomainByType[entry.Type] {
					t.Errorf("%s %s discipline order regressed at spec %s", key, entry.Type, entry.ID)
				}
				seenType[entry.Type] = true
				lastDomainByType[entry.Type] = order
			}
			for discipline, quotas := range profile.DisciplineQuota {
				for questionType, want := range quotas {
					if domainScores[discipline][questionType] != want {
						t.Errorf("%s %s %s score = %.1f; want %.1f", key, discipline, questionType, domainScores[discipline][questionType], want)
					}
				}
			}
			for discipline, quotas := range profile.DisciplineCountQuota {
				for questionType, want := range quotas {
					if domainCounts[discipline][questionType] != want {
						t.Errorf("%s %s %s count = %d; want %d", key, discipline, questionType, domainCounts[discipline][questionType], want)
					}
				}
			}
		}
		if key == "english1" {
			// English I has fixed per-section scores (cloze 0.5, reading 2,
			// translation 2×5, application letter 10, essay 20). An invented
			// half-point score would create a question type the paper has none of.
			mix := make(map[string]int)
			for _, entry := range spec.Entries {
				mix[fmt.Sprintf("%s/%.1f", entry.Type, entry.Score)]++
			}
			want := map[string]int{"choice/0.5": 20, "choice/2.0": 25, "major/2.0": 5, "major/10.0": 1, "major/20.0": 1}
			for shape, count := range want {
				if mix[shape] != count {
					t.Errorf("english1 %s count = %d; want %d (all=%v)", shape, mix[shape], count, mix)
				}
			}
		}
	}
}

// disciplineOrderRank returns the position of discipline in the blueprint order,
// or len(order) when it is absent (unknown labels sort last).
func disciplineOrderRank(order []string, discipline string) int {
	for i, item := range order {
		if item == discipline {
			return i
		}
	}
	return len(order)
}

// TestStrictDifficultyPresetsHitTargetsForPrioritySubjects pins the strict
// difficulty path for 408 and both math subjects: the requested easy/medium/hard
// histogram must be met exactly, not approximated, and the official type and
// discipline structure must survive the band allocation.
func TestStrictDifficultyPresetsHitTargetsForPrioritySubjects(t *testing.T) {
	if os.Getenv("RUN_CORPUS_TESTS") != "1" {
		t.Skip("set RUN_CORPUS_TESTS=1 to compose against bundled corpora")
	}
	cases := []struct {
		subject, preset, counts string
	}{
		{subject: "408", preset: "hard"},
		{subject: "408", counts: "14,24,9"},
		{subject: "math1", preset: "hard"},
		{subject: "math2", preset: "easy"},
		{subject: "math2", counts: "2,10,10"},
	}
	a := newTestApp(t)
	for _, tc := range cases {
		name := tc.subject + "_" + tc.preset + tc.counts
		t.Run(name, func(t *testing.T) {
			profile, _ := subjectProfile(tc.subject)
			parsed, err := a.parser.Parse(t.Context(), repoPath("data", profile.Source))
			if err != nil {
				t.Fatal(err)
			}
			bp, err := blueprintForSubjectDifficulty(tc.subject, tc.preset, "", tc.counts)
			if err != nil {
				t.Fatal(err)
			}
			if !bp.DifficultyCountStrict || len(bp.DiffCountQuota) == 0 {
				t.Fatalf("preset %q/%q did not activate the strict difficulty contract", tc.preset, tc.counts)
			}
			spec, err := a.orch.Compose(t.Context(), &bp, parsed.Questions)
			if err != nil {
				t.Fatalf("compose %s: %v", name, err)
			}
			bands := make(map[models.DifficultyBand]int)
			counts := make(map[models.QuestionType]int)
			for _, entry := range spec.Entries {
				bands[entry.DiffBand]++
				counts[entry.Type]++
			}
			for band, want := range bp.DiffCountQuota {
				if bands[band] != want {
					t.Errorf("%s band %d = %d, want %d (all=%v)", name, band, bands[band], want, bands)
				}
			}
			for questionType, want := range profile.TypeCountQuota {
				if counts[questionType] != want {
					t.Errorf("%s type %s = %d, want %d", name, questionType, counts[questionType], want)
				}
			}
			if math.Abs(spec.TotalScore-profile.TotalScore) > 0.5 {
				t.Errorf("%s total = %.0f, want %.0f", name, spec.TotalScore, profile.TotalScore)
			}
		})
	}
}
