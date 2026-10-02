package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"GoT0GenPaper/internal/config"
	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/grader"
	"GoT0GenPaper/internal/pipeline/scan"
	"GoT0GenPaper/internal/pipeline/validator"
)

const cliUsage = `GoT0GenPaper 命令行工具

用法:
  got0genpaper version
  got0genpaper generate --subject 408 [--difficulty medium] [--source PATH]
  got0genpaper repair [--specs spec_001,spec_002] [--compile]
  got0genpaper test-provider [--provider ID] [--base-url URL] [--model MODEL] [--vision]
  got0genpaper test [--network]
  got0genpaper health | artifacts | compile
  got0genpaper grade --paper PATH --answers PATH [--output PATH]
  got0genpaper scan --paper PATH --images PATH... [--output PATH]
  got0genpaper grade-scan --paper PATH --images PATH... [--output PATH]
  got0genpaper config list|show|use|add|remove

环境变量:
  GOT0GENPAPER_CONFIG  配置文件路径
  NEWAPI_API_KEY       覆盖当前供应商 API Key
  NEWAPI_BASE_URL      覆盖当前供应商 Base URL
  NEWAPI_MODEL_DEFAULT 覆盖当前供应商默认模型
`

var version = "dev"

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(cliUsage)
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Println("got0genpaper", version)
		return nil
	case "config":
		return runConfigCommand(args[1:])
	case "test-provider":
		return runTestProvider(args[1:])
	case "test":
		return runSelfTest(args[1:])
	case "health":
		return runHealth()
	case "artifacts":
		return runArtifacts()
	case "generate":
		return runGenerate(args[1:])
	case "repair":
		return runRepair(args[1:])
	case "grade":
		return runGrade(args[1:])
	case "scan":
		return runScan(args[1:], false)
	case "grade-scan":
		return runScan(args[1:], true)
	case "compile":
		return runCompile()
	default:
		return fmt.Errorf("未知命令 %q，运行 got0genpaper help 查看用法", args[0])
	}
}

func runRepair(args []string) error {
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
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
	a := newCLIAPI()
	result, err := a.RepairStoredPaper(ids)
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
		statuses, compileErr := a.CompilePaperPDF()
		if err := printJSON(statuses); err != nil {
			return err
		}
		return compileErr
	}
	return nil
}

func runScan(args []string, grade bool) error {
	name := "scan"
	if grade {
		name = "grade-scan"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	paperPath := fs.String("paper", "", "paper.json 路径")
	images := imagePathsFlag{}
	fs.Var(&images, "images", "答题卡扫描图路径，可重复传入或使用逗号分隔")
	outputPath := fs.String("output", "", "JSON 输出路径")
	segmentsDir := fs.String("segments-dir", "", "答题区域分割图输出目录；不传则使用临时目录")
	allowUncalibrated := fs.Bool("allow-uncalibrated", false, "仅诊断用途：允许没有定位标记的页面进入未校准模式")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Accept both repeated `--images path` and the documented shorthand
	// `--images path1 path2`; the standard flag package leaves the latter
	// positional paths in Args after the first value.
	for _, path := range fs.Args() {
		if err := images.Set(path); err != nil {
			return err
		}
	}
	if *paperPath == "" || len(images) == 0 {
		return fmt.Errorf("%s 需要 --paper 和至少一个 --images", name)
	}
	var paper models.ExamPaper
	if err := readJSON(*paperPath, &paper); err != nil {
		return fmt.Errorf("读取试卷: %w", err)
	}
	result, err := scan.Run(context.Background(), paper, images, scan.ScanOptions{AllowUncalibrated: *allowUncalibrated, SegmentsDir: *segmentsDir})
	if err != nil {
		return err
	}
	var value interface{} = result
	if grade {
		a := newCLIAPI()
		var client *llm.Client
		model := ""
		if provider := a.cfg.ActiveProvider(); provider != nil {
			model = provider.VisionModel
			if model == "" {
				model = firstNonEmpty(provider.JudgeModel, provider.DefaultModel)
			}
			if strings.TrimSpace(provider.APIKey) != "" || isLocalProvider(provider.BaseURL) {
				client = a.llmC
			}
		}
		value = scan.GradeScanWithVision(context.Background(), paper, *result, client, model)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if *outputPath == "" {
		fmt.Println(string(data))
		return nil
	}
	if err := os.WriteFile(*outputPath, data, 0644); err != nil {
		return err
	}
	fmt.Println(*outputPath)
	return nil
}

type imagePathsFlag []string

func (f *imagePathsFlag) String() string { return strings.Join(*f, ",") }

func (f *imagePathsFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		if strings.TrimSpace(part) != "" {
			*f = append(*f, strings.TrimSpace(part))
		}
	}
	return nil
}

func newCLIAPI() *API {
	a := NewAPI()
	a.ctx = context.Background()
	return a
}

func runHealth() error {
	return printJSON(newCLIAPI().HealthCheck())
}

func runArtifacts() error {
	artifacts, err := newCLIAPI().ListArtifacts()
	if err != nil {
		return err
	}
	return printJSON(artifacts)
}

func runTestProvider(args []string) error {
	fs := flag.NewFlagSet("test-provider", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	providerID := fs.String("provider", "", "配置中的供应商 ID")
	baseURL := fs.String("base-url", "", "OpenAI 兼容 Base URL")
	apiKey := fs.String("api-key", "", "API Key；未传入时使用配置")
	model := fs.String("model", "", "测试模型")
	vision := fs.Bool("vision", false, "发送最小 PNG 图像，测试多模态能力")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := newCLIAPI()
	provider, err := providerForCLI(a.cfg, *providerID)
	if err != nil {
		return err
	}
	req := ProviderConnectionRequest{BaseURL: provider.BaseURL, APIKey: provider.APIKey, Model: provider.DefaultModel}
	if *baseURL != "" {
		req.BaseURL = *baseURL
	}
	if *apiKey != "" {
		req.APIKey = *apiKey
	}
	if *model != "" {
		req.Model = *model
	}
	if *vision && *model == "" {
		req.Model = firstNonEmpty(provider.VisionModel, provider.DefaultModel)
	}
	result := a.TestProviderConnection(req)
	if *vision {
		result = a.TestProviderVisionConnection(req)
	}
	result.ProviderName = provider.Name
	if err := printJSON(result); err != nil {
		return err
	}
	if !result.OK {
		return errors.New(result.Message)
	}
	return nil
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
	a := newCLIAPI()
	blueprint, err := a.BlueprintForSubjectWithDifficultyConfig(*subject, *difficulty, *difficultyHist, *difficultyCounts)
	if err != nil {
		return err
	}
	summary, err := a.RunPipeline(PipelineRequest{Source: *source, Blueprint: blueprint, MaxRepairRounds: *maxRounds})
	if err != nil {
		return err
	}
	result := struct {
		Subject          string             `json:"subject"`
		ParseCount       int                `json:"parseCount"`
		SpecCount        int                `json:"specCount"`
		Generated        int                `json:"generated"`
		Answered         int                `json:"answered"`
		RepairRounds     int                `json:"repairRounds"`
		Validation       interface{}        `json:"validation"`
		OutputDir        string             `json:"outputDir"`
		Difficulty       string             `json:"difficulty"`
		DifficultyHist   map[string]float64 `json:"difficultyHist"`
		DifficultyCounts map[string]int     `json:"difficultyCounts"`
		Warnings         []string           `json:"warnings"`
	}{*subject, summary.ParseCount, summary.SpecCount, summary.Generated, summary.Answered, summary.RepairRounds, summary.Validation, a.store.OutputDir, *difficulty, difficultyHistogramView(blueprint.DiffHist), difficultyCountView(blueprint.DiffCountQuota), summary.Warnings}
	if err := printJSON(result); err != nil {
		return err
	}
	if *compile {
		statuses, compileErr := a.CompilePaperPDF()
		if err := printJSON(statuses); err != nil {
			return err
		}
		return compileErr
	}
	return nil
}

func runGrade(args []string) error {
	fs := flag.NewFlagSet("grade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	paperPath := fs.String("paper", "", "paper.json 路径")
	answersPath := fs.String("answers", "", "学生答案 JSON 路径")
	outputPath := fs.String("output", "", "评分结果输出路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *paperPath == "" || *answersPath == "" {
		return errors.New("grade 需要 --paper 和 --answers")
	}
	a := newCLIAPI()
	var paper models.ExamPaper
	var requests []grader.GradeRequest
	if err := readJSON(*paperPath, &paper); err != nil {
		return fmt.Errorf("读取试卷: %w", err)
	}
	if err := readJSON(*answersPath, &requests); err != nil {
		return fmt.Errorf("读取学生答案: %w", err)
	}
	provided := make(map[string]grader.GradeRequest, len(requests))
	duplicates := make(map[string]bool)
	for _, request := range requests {
		if _, exists := provided[request.QuestionID]; exists {
			duplicates[request.QuestionID] = true
			continue
		}
		provided[request.QuestionID] = request
	}
	paperIDs := make(map[string]struct{}, len(paper.Questions))
	for _, q := range paper.Questions {
		paperIDs[q.ID] = struct{}{}
	}
	normalized := make([]grader.GradeRequest, 0, len(paper.Questions)+len(requests))
	for _, q := range paper.Questions {
		request, ok := provided[q.ID]
		if duplicates[q.ID] {
			request = grader.GradeRequest{QuestionID: q.ID, AnswerStatus: "needs_review"}
		} else if !ok {
			request = grader.GradeRequest{QuestionID: q.ID, AnswerStatus: "blank"}
		}
		request.QuestionID = q.ID
		request.QuestionStem = firstNonEmpty(request.QuestionStem, q.Stem)
		request.MaxScore = firstPositive(request.MaxScore, q.Score)
		request.QuestionType = string(q.Type)
		if q.Type == models.TypeChoice || q.Type == models.TypeMultiChoice {
			request.CorrectAnswer = q.Answer
		}
		normalized = append(normalized, request)
		delete(provided, q.ID)
	}
	// Keep unknown IDs in the report instead of silently dropping malformed
	// submissions; GradeAll marks them needs_review. Known duplicates were
	// represented above by one explicit needs_review request.
	for _, request := range requests {
		if _, known := paperIDs[request.QuestionID]; !known {
			normalized = append(normalized, request)
		}
	}
	requests = normalized
	results, err := a.GradeAnswers(requests, paper.Rubrics)
	if err != nil {
		return err
	}
	totalScore := 0.0
	maxScore := 0.0
	needsReview := make([]string, 0)
	for _, result := range results {
		totalScore += result.TotalScore
		for _, q := range paper.Questions {
			if q.ID == result.QuestionID {
				maxScore += q.Score
				break
			}
		}
		if result.Status == "needs_review" {
			needsReview = append(needsReview, result.QuestionID)
		}
	}
	report := struct {
		Results     []grader.GradeResult `json:"results"`
		TotalScore  float64              `json:"totalScore"`
		MaxScore    float64              `json:"maxScore"`
		NeedsReview []string             `json:"needsReview,omitempty"`
	}{results, totalScore, maxScore, needsReview}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if *outputPath == "" {
		fmt.Println(string(data))
		return nil
	}
	if err := os.WriteFile(*outputPath, data, 0644); err != nil {
		return err
	}
	fmt.Println(*outputPath)
	return nil
}

func runCompile() error {
	statuses, err := newCLIAPI().CompilePaperPDF()
	if printErr := printJSON(statuses); printErr != nil {
		return printErr
	}
	return err
}

func runConfigCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("config 需要 list、show、use、add 或 remove")
	}
	a := newCLIAPI()
	switch args[0] {
	case "list", "show":
		return printConfig(a.cfg, args[0] == "list")
	case "use":
		if len(args) != 2 {
			return errors.New("用法: config use PROVIDER_ID")
		}
		if a.cfg.ProviderByID(args[1]) == nil {
			return fmt.Errorf("供应商 %q 不存在", args[1])
		}
		if provider := a.cfg.ProviderByID(args[1]); !provider.Enabled {
			return fmt.Errorf("供应商 %q 已禁用", args[1])
		}
		a.cfg.Providers.ActiveID = args[1]
		return a.cfg.Save(config.ConfigPath())
	case "add":
		return runConfigAdd(a.cfg, args[1:])
	case "remove":
		return runConfigRemove(a.cfg, args[1:])
	default:
		return fmt.Errorf("未知 config 操作 %q", args[0])
	}
}

func runConfigAdd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("config add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	id := fs.String("id", "", "供应商 ID")
	name := fs.String("name", "", "供应商名称")
	baseURL := fs.String("base-url", "", "Base URL")
	apiKey := fs.String("api-key", "", "API Key")
	model := fs.String("model", "", "默认模型")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *baseURL == "" || *model == "" {
		return errors.New("config add 需要 --id、--base-url 和 --model")
	}
	if cfg.ProviderByID(*id) != nil {
		return fmt.Errorf("供应商 %q 已存在", *id)
	}
	cfg.Providers.Providers = append(cfg.Providers.Providers, config.ProviderConfig{ID: *id, Name: firstNonEmpty(*name, *id), BaseURL: *baseURL, APIKey: *apiKey, ModelByType: map[string]string{"choice": *model, "fill_blank": *model, "major": *model}, JudgeModel: *model, VisionModel: *model, DefaultModel: *model, Enabled: true})
	return cfg.Save(config.ConfigPath())
}

func runConfigRemove(cfg *config.Config, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: config remove PROVIDER_ID")
	}
	if len(cfg.Providers.Providers) <= 1 {
		return errors.New("至少保留一个供应商")
	}
	filtered := cfg.Providers.Providers[:0]
	found := false
	for _, p := range cfg.Providers.Providers {
		if p.ID == args[0] {
			found = true
			continue
		}
		filtered = append(filtered, p)
	}
	if !found {
		return fmt.Errorf("供应商 %q 不存在", args[0])
	}
	cfg.Providers.Providers = filtered
	if cfg.Providers.ActiveID == args[0] {
		cfg.Providers.ActiveID = filtered[0].ID
	}
	return cfg.Save(config.ConfigPath())
}

func providerForCLI(cfg *config.Config, id string) (*config.ProviderConfig, error) {
	if id == "" {
		p := cfg.ActiveProvider()
		if p == nil {
			return nil, errors.New("没有可用的当前供应商")
		}
		return p, nil
	}
	p := cfg.ProviderByID(id)
	if p == nil {
		return nil, fmt.Errorf("供应商 %q 不存在", id)
	}
	return p, nil
}

func printConfig(cfg *config.Config, listOnly bool) error {
	type view struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		BaseURL      string `json:"baseUrl"`
		DefaultModel string `json:"defaultModel"`
		VisionModel  string `json:"visionModel"`
		Enabled      bool   `json:"enabled"`
		Active       bool   `json:"active"`
	}
	views := make([]view, 0, len(cfg.Providers.Providers))
	for _, p := range cfg.Providers.Providers {
		views = append(views, view{p.ID, p.Name, p.BaseURL, p.DefaultModel, p.VisionModel, p.Enabled, p.ID == cfg.Providers.ActiveID})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	if listOnly {
		return printJSON(struct {
			ActiveID  string `json:"activeId"`
			Providers []view `json:"providers"`
		}{cfg.Providers.ActiveID, views})
	}
	return printJSON(struct {
		ConfigPath string             `json:"configPath"`
		ActiveID   string             `json:"activeId"`
		Providers  []view             `json:"providers"`
		Paths      config.PathsConfig `json:"paths"`
	}{config.ConfigPath(), cfg.Providers.ActiveID, views, cfg.Paths})
}

func printJSON(value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
func readJSON(path string, target interface{}) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func firstPositive(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}
