package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/orchestrator"
	"GoT0GenPaper/internal/pipeline/validator"
	"GoT0GenPaper/internal/storage"
)

func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	paperPath := fs.String("paper", "", "指定已装配的 paper.json；留空使用当前 output/paper.json")
	dataPath := fs.String("data", "", "指定 parse_result.json、blueprint.json 所在目录；留空使用当前配置的数据目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("validate 不接受位置参数")
	}
	a := newCLIApp()
	if strings.TrimSpace(*paperPath) != "" {
		if err := configurePaperStore(a, *paperPath, *dataPath); err != nil {
			return err
		}
	} else if strings.TrimSpace(*dataPath) != "" {
		return fmt.Errorf("未指定 --paper 时不能单独使用 --data")
	}
	paper, err := a.loadStoredPaper()
	if err != nil {
		return fmt.Errorf("读取已装配试卷: %w", err)
	}
	dataExplicit := strings.TrimSpace(*dataPath) != ""
	parseAvailable := true
	if err := a.loadStoredParse(); err != nil {
		parseAvailable = false
		if dataExplicit {
			return fmt.Errorf("读取历年真题语料以进行去重校验: %w", err)
		}
		fmt.Fprintf(os.Stderr, "提示：归档目录没有 parse_result.json，跳过去重基准加载，仅执行确定性试卷校验\n")
	}
	var blueprint orchestrator.Blueprint
	blueprintAvailable := true
	if err := a.store.LoadDataJSON("blueprint", &blueprint); err != nil {
		blueprintAvailable = false
		if dataExplicit {
			return fmt.Errorf("读取组卷蓝图: %w", err)
		}
		fmt.Fprintf(os.Stderr, "提示：归档目录没有 blueprint.json，跳过覆盖/蓝图难度校验，仅执行确定性试卷校验\n")
	}
	var validationBlueprint *orchestrator.Blueprint
	if blueprintAvailable {
		validationBlueprint = &blueprint
	}
	validation, err := a.validator.Validate(a.ctx, *paper, validationBlueprint)
	if err != nil {
		return fmt.Errorf("整卷校验: %w", err)
	}
	if err := a.store.SaveOutputJSON("validation", validation); err != nil {
		return fmt.Errorf("保存校验报告: %w", err)
	}
	return printJSON(struct {
		Subject         string                      `json:"subject"`
		Validation      *validator.ValidationResult `json:"validation"`
		OutputDir       string                      `json:"outputDir"`
		ParseLoaded     bool                        `json:"parseLoaded"`
		BlueprintLoaded bool                        `json:"blueprintLoaded"`
	}{paper.Subject, validation, a.store.OutputDir, parseAvailable, blueprintAvailable})
}

func runReassemble(args []string) error {
	fs := flag.NewFlagSet("reassemble", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	paperPath := fs.String("paper", "", "指定已装配的 paper.json；留空使用当前 output/paper.json")
	compile := fs.Bool("compile", false, "重装配后编译试卷 PDF")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("reassemble 不接受位置参数")
	}
	a := newCLIApp()
	if strings.TrimSpace(*paperPath) != "" {
		if err := configurePaperStore(a, *paperPath, ""); err != nil {
			return err
		}
	}
	paper, err := a.loadStoredPaper()
	if err != nil {
		return fmt.Errorf("读取已装配试卷: %w", err)
	}
	if err := normalizeEnglishPaperOrder(paper); err != nil {
		return err
	}
	if _, err := a.assemblePaper(*paper); err != nil {
		return fmt.Errorf("重新装配试卷: %w", err)
	}
	if err := printJSON(struct {
		Subject   string `json:"subject"`
		Questions int    `json:"questions"`
		OutputDir string `json:"outputDir"`
	}{paper.Subject, len(paper.Questions), a.store.OutputDir}); err != nil {
		return err
	}
	if *compile {
		statuses, compileErr := a.compilePaperPDF()
		if err := printJSON(statuses); err != nil {
			return err
		}
		return compileErr
	}
	return nil
}

func normalizeEnglishPaperOrder(paper *models.ExamPaper) error {
	if paper == nil || !models.IsEnglishSubject(paper.Subject) {
		return nil
	}
	entriesByID := make(map[string]models.SpecEntry, len(paper.SpecTable.Entries))
	for _, entry := range paper.SpecTable.Entries {
		entriesByID[entry.ID] = entry
	}
	sort.SliceStable(paper.Questions, func(i, j int) bool {
		si, pi := models.EnglishQuestionOrder(paper.Questions[i].Points)
		sj, pj := models.EnglishQuestionOrder(paper.Questions[j].Points)
		if si != sj {
			return si < sj
		}
		if pi != pj {
			return pi < pj
		}
		return paper.Questions[i].ID < paper.Questions[j].ID
	})
	entries := make([]models.SpecEntry, 0, len(paper.Questions))
	for i := range paper.Questions {
		question := &paper.Questions[i]
		if len(question.Points) == 0 {
			return fmt.Errorf("英语题目 %s 缺少分区知识点，无法重排", question.ID)
		}
		question.Points = append([]string(nil), question.Points...)
		if strings.EqualFold(strings.TrimSpace(paper.Subject), "english2") || strings.TrimSpace(paper.Subject) == "英语二" {
			if len(question.Points) >= 4 && strings.EqualFold(strings.TrimSpace(question.Points[1]), "阅读理解") &&
				strings.EqualFold(strings.TrimSpace(question.Points[2]), "Part B") {
				question.Points[3] = "人物观点匹配"
			}
		}
		question.Points[len(question.Points)-1] = fmt.Sprintf("题位%d", i+1)
		entry, ok := entriesByID[question.SpecID]
		if !ok {
			return fmt.Errorf("英语题目 %s 找不到对应细目表条目 %s", question.ID, question.SpecID)
		}
		entry.Index = i + 1
		entry.Points = append([]string(nil), question.Points...)
		entries = append(entries, entry)
	}
	paper.SpecTable.Entries = entries
	paper.SpecTable.Subject = paper.Subject
	return nil
}

func runRepair(args []string) error {
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	paperPath := fs.String("paper", "", "指定已装配的 paper.json；留空使用当前 output/paper.json")
	dataPath := fs.String("data", "", "指定 parse_result.json、blueprint.json 所在目录；留空使用 paper.json 所在目录")
	specs := fs.String("specs", "", "只修复这些 spec 题位，逗号分隔；留空则按校验结果选择")
	compile := fs.Bool("compile", false, "修复后尝试编译 PDF")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var ids []string
	for _, id := range strings.Split(*specs, ",") {
		if strings.TrimSpace(id) != "" {
			ids = append(ids, strings.TrimSpace(id))
		}
	}
	a := newCLIApp()
	if err := configurePaperStore(a, *paperPath, *dataPath); err != nil {
		return err
	}
	result, err := a.repairStoredPaper(ids)
	if err != nil {
		return err
	}
	view := struct {
		Rounds     int                         `json:"rounds"`
		Validation *validator.ValidationResult `json:"validation"`
		OutputDir  string                      `json:"outputDir"`
	}{result.Rounds, result.Validation, a.store.OutputDir}
	if err := printJSON(view); err != nil {
		return err
	}
	if *compile {
		statuses, compileErr := a.compilePaperPDF()
		if err := printJSON(statuses); err != nil {
			return err
		}
		return compileErr
	}
	return nil
}

func configurePaperStore(a *app, paperPath, dataPath string) error {
	if strings.TrimSpace(paperPath) == "" {
		return nil
	}
	absPaper, err := filepath.Abs(filepath.Clean(paperPath))
	if err != nil {
		return fmt.Errorf("解析 paper 路径: %w", err)
	}
	if filepath.Base(absPaper) != "paper.json" {
		return fmt.Errorf("--paper 必须指向 paper.json，收到 %q", paperPath)
	}
	if _, err := os.Stat(absPaper); err != nil {
		return fmt.Errorf("读取指定 paper: %w", err)
	}
	outDir := filepath.Dir(absPaper)
	dataDir := outDir
	if strings.TrimSpace(dataPath) != "" {
		dataDir, err = filepath.Abs(filepath.Clean(dataPath))
		if err != nil {
			return fmt.Errorf("解析数据目录路径: %w", err)
		}
	}
	store, err := storage.New(dataDir, outDir, a.cfg.Paths.Templates)
	if err != nil {
		return fmt.Errorf("初始化试卷存储: %w", err)
	}
	a.store = store
	a.storageErr = nil
	a.lastParse = nil
	a.lastBlueprint = nil
	return nil
}

func newCLIApp() *app {
	a := newApp()
	a.ctx = context.Background()
	return a
}

func runGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	subject := fs.String("subject", "408", "科目：408、数学一、数学二、英语一、英语二、政治")
	difficulty := fs.String("difficulty", "default", "难度预设：default、easy、medium、hard")
	difficultyHist := fs.String("difficulty-hist", "", "自定义难度比例：easy,medium,hard，例如 0.2,0.6,0.2")
	difficultyCounts := fs.String("difficulty-counts", "", "自定义难度题数：easy,medium,hard，例如 2,10,10；总和必须等于该科目题数")
	source := fs.String("source", "", "真题目录或文件；默认使用 dataDir 下的科目目录")
	maxRounds := fs.Int("max-repair-rounds", 1, "整卷修复最多轮数；0 表示只生成首轮并校验，不自动修复")
	compile := fs.Bool("compile", false, "组卷后尝试编译 PDF")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := newCLIApp()
	blueprint, err := blueprintForSubjectDifficulty(*subject, *difficulty, *difficultyHist, *difficultyCounts)
	if err != nil {
		return err
	}
	summary, err := a.runPipeline(PipelineRequest{Source: *source, Blueprint: blueprint, MaxRepairRounds: *maxRounds})
	if err != nil {
		return err
	}
	result := struct {
		Subject          string             `json:"subject"`
		ParseCount       int                `json:"parseCount"`
		SpecCount        int                `json:"specCount"`
		Generated        int                `json:"generated"`
		RepairRounds     int                `json:"repairRounds"`
		Validation       interface{}        `json:"validation"`
		OutputDir        string             `json:"outputDir"`
		Difficulty       string             `json:"difficulty"`
		DifficultyHist   map[string]float64 `json:"difficultyHist"`
		DifficultyCounts map[string]int     `json:"difficultyCounts"`
		Warnings         []string           `json:"warnings"`
	}{*subject, summary.ParseCount, summary.SpecCount, summary.Generated, summary.RepairRounds, summary.Validation, a.store.OutputDir, *difficulty, difficultyHistogramView(blueprint.DiffHist), difficultyCountView(blueprint.DiffCountQuota), summary.Warnings}
	if err := printJSON(result); err != nil {
		return err
	}
	if *compile {
		statuses, compileErr := a.compilePaperPDF()
		if err := printJSON(statuses); err != nil {
			return err
		}
		return compileErr
	}
	return nil
}

func runCompile() error {
	statuses, err := newCLIApp().compilePaperPDF()
	if printErr := printJSON(statuses); printErr != nil {
		return printErr
	}
	return err
}
