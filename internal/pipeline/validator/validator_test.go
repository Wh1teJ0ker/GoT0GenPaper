package validator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
)

func TestValidateRejectsEmptyPaper(t *testing.T) {
	result, err := New(nil).Validate(context.Background(), models.ExamPaper{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StructureOK || result.ContentReady || result.CoverageOK || result.DifficultyOK || result.ScoreTotalOK {
		t.Fatalf("empty paper passed validation: %+v", result)
	}
	if len(result.StructureIssues) == 0 {
		t.Fatal("empty paper did not report a structural issue")
	}
}

func TestValidateRejectsInvalidQuestionShape(t *testing.T) {
	paper := models.ExamPaper{
		Questions: []models.GeneratedQuestion{
			{ID: "q1", Type: models.TypeChoice, Score: 0},
			{ID: "q1", Type: models.QuestionType("unknown"), Score: 2},
		},
		TotalScore: 2,
	}
	result, err := New(nil).Validate(context.Background(), paper, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StructureOK || len(result.StructureIssues) < 3 {
		t.Fatalf("invalid question shape was not rejected: %+v", result)
	}
}

func TestValidateAcceptsMissingTypedBlueprintWithoutPanic(t *testing.T) {
	var blueprint *orchestrator.Blueprint
	paper := models.ExamPaper{
		Subject:    "英语二",
		Questions:  []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2, Difficulty: 0.5, Stem: "题面", Answer: "答案：A。根据上下文可知。"}},
		TotalScore: 2,
	}
	result, err := New(nil).Validate(context.Background(), paper, blueprint)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("typed nil blueprint returned nil result")
	}
}

func TestEnglish2StructureRequires2026CompositeGroups(t *testing.T) {
	paper := english2StructureFixture()
	if issues := English2StructureIssues(paper); len(issues) != 0 {
		t.Fatalf("valid 2026 English II fixture rejected: %v", issues)
	}

	broken := english2StructureFixture()
	broken.Questions[21].GroupID = "english2-reading-2"
	broken.Questions[42].Options = []string{"A", "B", "C", "D"}
	issues := English2StructureIssues(broken)
	joined := strings.Join(issues, " ")
	if !strings.Contains(joined, "题位 22 未绑定题组") || !strings.Contains(joined, "统一的七个共享观点陈述") {
		t.Fatalf("independent reading/Part B options were accepted: %v", issues)
	}
}

func TestEnglish2StructureRejectsDuplicateClozeMarkerAndPartBNamedViewpointFormat(t *testing.T) {
	paper := english2StructureFixture()
	paper.Questions[0].SharedMaterial = strings.Replace(paper.Questions[0].SharedMaterial, "[1]", "[10]", 1)
	for position := 1; position <= 20; position++ {
		paper.Questions[position-1].SharedMaterial = paper.Questions[0].SharedMaterial
	}
	for position := 41; position <= 45; position++ {
		paper.Questions[position-1].Stem = fmt.Sprintf("Dr Expert %d", position-40)
	}
	issues := strings.Join(English2StructureIssues(paper), " ")
	for _, want := range []string{"完形填空共用文章必须恰有一个 [1] 空位", "必须填写一位完整英文姓名"} {
		if !strings.Contains(issues, want) {
			t.Fatalf("invalid paper did not report %q: %s", want, issues)
		}
	}
}

func TestEnglish2StructureRejectsWrongSectionPathsAndMajorGroupMaterial(t *testing.T) {
	t.Run("part A text path", func(t *testing.T) {
		paper := english2StructureFixture()
		paper.Questions[20].Points[2] = "Part B"
		issues := strings.Join(English2StructureIssues(paper), " ")
		if !strings.Contains(issues, "题位 21 必须属于阅读理解 Part A Text 1") {
			t.Fatalf("wrong Part A path was accepted: %s", issues)
		}
	})

	t.Run("translation path", func(t *testing.T) {
		paper := english2StructureFixture()
		paper.Questions[45].Points[1] = "写作"
		issues := strings.Join(English2StructureIssues(paper), " ")
		if !strings.Contains(issues, "题位 46 必须是翻译") {
			t.Fatalf("wrong translation path was accepted: %s", issues)
		}
	})

	t.Run("major questions cannot carry a group", func(t *testing.T) {
		paper := english2StructureFixture()
		paper.Questions[46].GroupID = "english2-reading-1"
		paper.Questions[46].SharedMaterial = strings.Repeat("A shared passage with enough words. ", 60)
		issues := strings.Join(English2StructureIssues(paper), " ")
		if !strings.Contains(issues, "题位 47 属于翻译/写作，不应携带复合题组材料") {
			t.Fatalf("major group material was accepted: %s", issues)
		}
	})
}

func english2StructureFixture() models.ExamPaper {
	paper := models.ExamPaper{Subject: "英语二", TotalScore: 100}
	cloze := strings.Repeat("A connected passage introduces a topic and then develops it with examples from everyday life. ", 24)
	for number := 1; number <= 20; number++ {
		cloze += fmt.Sprintf(" [%d]", number)
	}
	longArticle := strings.Repeat("Community learning helps residents exchange ideas and respond carefully to local needs. ", 24)
	partBMaterial := longArticle + " Ramni Jannadass identifies the shortage of planting material as a major bottleneck. Christopher Kettle explains why infrastructure for collecting and storing seeds is essential. John Stanturf warns that fast-multiplying forest creatures can threaten restoration. Pedro Brancalion compares global restoration goals with the practical needs of local communities. Robin Chazdon stresses that restoration is about people as well as trees."
	headings := []string{"Seed supply is a central bottleneck", "Infrastructure must support restoration", "Fast-multiplying threats require attention", "Local needs may differ from global goals", "People matter as much as trees", "Private funding solves every problem", "Restoration needs no local capacity"}
	partBTargets := []string{"Ramni Jannadass", "Christopher Kettle", "John Stanturf", "Pedro Brancalion", "Robin Chazdon"}
	for position := 1; position <= 48; position++ {
		question := models.GeneratedQuestion{ID: fmt.Sprintf("q%d", position), Type: models.TypeChoice, Score: 2, Difficulty: 0.55, Status: "ready", Stem: "Choose the best answer.", Answer: "答案：A。根据材料可知，A 项符合中心意思。"}
		switch {
		case position <= 20:
			question.Score = 0.5
			question.GroupID, question.SharedMaterial = "english2-cloze", cloze
			question.Points = []string{"english2", "完形填空", "语篇词汇与语境", fmt.Sprintf("题位%d", position)}
			question.Options = []string{"take", "make", "keep", "find"}
		case position <= 40:
			textNumber := (position-21)/5 + 1
			question.GroupID, question.SharedMaterial = fmt.Sprintf("english2-reading-%d", textNumber), longArticle
			question.Points = []string{"english2", "阅读理解", "Part A", fmt.Sprintf("Text %d", textNumber), fmt.Sprintf("题位%d", position)}
			question.Options = []string{"Choice A", "Choice B", "Choice C", "Choice D"}
		case position <= 45:
			question.GroupID, question.SharedMaterial = "english2-part-b", partBMaterial
			question.Points = []string{"english2", "阅读理解", "Part B", "人物观点匹配", fmt.Sprintf("题位%d", position)}
			question.Stem = partBTargets[position-41]
			question.Options, question.SharedOptions = append([]string(nil), headings...), append([]string(nil), headings...)
			question.Answer = fmt.Sprintf("答案：%c。该人物的观点明确支持对应陈述，结合共同材料中的具体表述，选项 %c 能准确概括其立场，其余选项与该人物观点不一致。", 'A'+rune(position-41), 'A'+rune(position-41))
		case position == 46:
			question.Type, question.Score = models.TypeMajor, 15
			question.Points = []string{"english2", "翻译", "英译汉", "题位46"}
			question.Stem = strings.Repeat("Careful discussion helps communities understand public choices and participate in decisions that shape their daily lives. ", 8)
		case position == 47:
			question.Type, question.Score = models.TypeMajor, 10
			question.Points = []string{"english2", "写作", "应用文", "题位47"}
			question.Stem = "Directions: Write an email to your friend and describe your experience."
		case position == 48:
			question.Type, question.Score = models.TypeMajor, 15
			question.Points = []string{"english2", "写作", "图表作文", "题位48"}
			question.Stem = "Directions: Write an essay based on the chart. The values are 55%, 42%, and 31%."
		}
		paper.Questions = append(paper.Questions, question)
	}
	return paper
}

func TestMissingCoverageMapsBackToSpecSlot(t *testing.T) {
	bp := &orchestrator.Blueprint{
		TotalScore:    10,
		CoverageFloor: map[string]float64{"missing": 1},
		DiffHist:      map[models.DifficultyBand]float64{models.BandMedium: 1},
	}
	paper := models.ExamPaper{
		Questions:  []models.GeneratedQuestion{{ID: "q1", SpecID: "spec_001", Type: models.TypeMajor, Score: 10, Difficulty: 0.5, Points: []string{"known"}}},
		SpecTable:  models.SpecTable{Entries: []models.SpecEntry{{ID: "spec_002", Points: []string{"missing"}}}},
		TotalScore: 10,
	}
	result, err := New(nil).Validate(context.Background(), paper, bp)
	if err != nil {
		t.Fatal(err)
	}
	if result.CoverageOK || len(result.MissingPoints) != 1 || result.MissingPoints[0] != "missing" {
		t.Fatalf("coverage result = %+v", result)
	}
	if !contains(result.ViolatedSpecIDs, "spec_002") {
		t.Fatalf("missing point was not mapped to spec slot: %v", result.ViolatedSpecIDs)
	}
}

func TestValidateReportsAndEnforcesDifficultyCounts(t *testing.T) {
	makePaper := func(difficulties ...float64) models.ExamPaper {
		paper := models.ExamPaper{
			Subject: "数学二",
			SpecTable: models.SpecTable{
				DifficultyTarget: map[models.DifficultyBand]int{
					models.BandEasy: 1, models.BandMedium: 1, models.BandHard: 1,
				},
			},
			TotalScore: 3,
		}
		for i, difficulty := range difficulties {
			paper.Questions = append(paper.Questions, models.GeneratedQuestion{
				ID: fmt.Sprintf("q%d", i), SpecID: fmt.Sprintf("spec_%d", i),
				Type: models.TypeMajor, Score: 1, Difficulty: difficulty,
				Stem: "题面", Answer: "答案", Status: "ready",
			})
		}
		return paper
	}

	matched, err := New(nil).Validate(context.Background(), makePaper(0.25, 0.5, 0.75), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !matched.DifficultyOK || matched.DifficultyCountsActual["easy"] != 1 || matched.DifficultyCountsTarget["hard"] != 1 {
		t.Fatalf("matched difficulty result = %+v", matched)
	}

	mismatched, err := New(nil).Validate(context.Background(), makePaper(0.25, 0.25, 0.75), nil)
	if err != nil {
		t.Fatal(err)
	}
	if mismatched.DifficultyOK || mismatched.DifficultyCountsActual["medium"] != 0 {
		t.Fatalf("mismatched difficulty result = %+v", mismatched)
	}
}

func TestValidateKeepsDefaultDifficultyAsSoftTarget(t *testing.T) {
	makePaper := func(strict bool) models.ExamPaper {
		paper := models.ExamPaper{
			Subject: "数学二",
			SpecTable: models.SpecTable{
				DifficultyTarget:       map[models.DifficultyBand]int{models.BandEasy: 2, models.BandMedium: 10, models.BandHard: 10},
				DifficultyTargetStrict: strict,
			},
			TotalScore: 22,
		}
		// 3/9/10 is close to 2/10/10 in ratio and is deliberately not an
		// exact integer match. It must pass only for the soft default mode.
		for i := 0; i < 22; i++ {
			difficulty := 0.5
			if i < 3 {
				difficulty = 0.25
				paper.Questions = append(paper.Questions, models.GeneratedQuestion{ID: fmt.Sprintf("q%d", i), Type: models.TypeMajor, Score: 1, Difficulty: difficulty, Stem: "题面", Answer: "答案", Status: "ready"})
				continue
			}
			band := models.BandMedium
			if i >= 12 {
				band = models.BandHard
			}
			value := 0.5
			if band == models.BandHard {
				value = 0.75
			}
			paper.Questions = append(paper.Questions, models.GeneratedQuestion{ID: fmt.Sprintf("q%d", i), Type: models.TypeMajor, Score: 1, Difficulty: value, Stem: "题面", Answer: "答案", Status: "ready"})
		}
		return paper
	}

	blueprint := &orchestrator.Blueprint{
		DiffHist: map[models.DifficultyBand]float64{
			models.BandEasy: 0.1, models.BandMedium: 0.45, models.BandHard: 0.45,
		},
		DiffCountQuota: map[models.DifficultyBand]int{
			models.BandEasy: 2, models.BandMedium: 10, models.BandHard: 10,
		},
		NumQuestions: 22,
	}
	soft, err := New(nil).Validate(context.Background(), makePaper(false), blueprint)
	if err != nil {
		t.Fatal(err)
	}
	if !soft.DifficultyOK {
		t.Fatalf("soft default target should use the ratio check: %+v", soft)
	}
	strictBlueprint := *blueprint
	strictBlueprint.DifficultyCountStrict = true
	strict, err := New(nil).Validate(context.Background(), makePaper(true), &strictBlueprint)
	if err != nil {
		t.Fatal(err)
	}
	if strict.DifficultyOK {
		t.Fatalf("strict target accepted a mismatched integer distribution: %+v", strict)
	}
}

func TestValidateRequiresReferenceAnswers(t *testing.T) {
	paper := models.ExamPaper{
		Questions:  []models.GeneratedQuestion{{ID: "q1", Type: models.TypeMajor, Score: 10, Stem: "题面", Status: "ready"}},
		TotalScore: 10,
	}
	result, err := New(nil).Validate(context.Background(), paper, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentReady {
		t.Fatalf("paper without a reference answer passed content validation: %+v", result)
	}
	if len(result.ContentIssues) == 0 {
		t.Fatal("missing reference answer was not reported")
	}
}

func TestValidateChecksDisciplineQuotasAndOrder(t *testing.T) {
	bp := &orchestrator.Blueprint{
		TotalScore: 10,
		TypeQuota: map[models.QuestionType]float64{
			models.TypeChoice: 10,
		},
		TypeCountQuota: map[models.QuestionType]int{
			models.TypeChoice: 2,
		},
		DisciplineQuota: map[string]map[models.QuestionType]float64{
			"高等数学": {models.TypeChoice: 5},
			"线性代数": {models.TypeChoice: 5},
		},
		DisciplineCountQuota: map[string]map[models.QuestionType]int{
			"高等数学": {models.TypeChoice: 1},
			"线性代数": {models.TypeChoice: 1},
		},
		DisciplineOrder: []string{"高等数学", "线性代数"},
	}
	makePaper := func(points ...[]string) models.ExamPaper {
		paper := models.ExamPaper{TotalScore: 10}
		for i, point := range points {
			paper.Questions = append(paper.Questions, models.GeneratedQuestion{
				ID: fmt.Sprintf("q%d", i+1), Type: models.TypeChoice, Score: 5,
				Difficulty: 0.5, Stem: fmt.Sprintf("question %d", i+1), Answer: "A",
				Options: []string{"a", "b", "c", "d"}, Points: point, Status: "ready",
			})
		}
		return paper
	}
	check := func(name string, paper models.ExamPaper, wantStructureOK bool, wantIssue string) {
		t.Helper()
		result, err := New(nil).Validate(context.Background(), paper, bp)
		if err != nil {
			t.Fatalf("%s: Validate returned error: %v", name, err)
		}
		if result.StructureOK != wantStructureOK {
			t.Errorf("%s: StructureOK = %v, want %v; issues=%v", name, result.StructureOK, wantStructureOK, result.StructureIssues)
		}
		if wantIssue != "" {
			found := false
			for _, issue := range result.StructureIssues {
				found = found || strings.Contains(issue, wantIssue)
			}
			if !found {
				t.Errorf("%s: missing issue containing %q: %v", name, wantIssue, result.StructureIssues)
			}
		}
	}

	check("balanced and ordered", makePaper([]string{"高等数学", "导数"}, []string{"线性代数", "矩阵"}), true, "")
	check("wrong balance", makePaper([]string{"高等数学", "导数"}, []string{"高等数学", "积分"}), false, "线性代数 的 choice 数量为 0")
	check("wrong order", makePaper([]string{"线性代数", "矩阵"}, []string{"高等数学", "导数"}), false, "学科顺序不符合蓝图")
	check("ODE is calculus", makePaper([]string{"常微分方程", "方程"}, []string{"线性代数", "矩阵"}), true, "")
}
