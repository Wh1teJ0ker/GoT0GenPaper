package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"GoT0GenPaper/internal/config"
	"GoT0GenPaper/internal/latex"
	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/assembler"
	"GoT0GenPaper/internal/pipeline/generator"
	"GoT0GenPaper/internal/pipeline/gnn"
	"GoT0GenPaper/internal/pipeline/orchestrator"
	"GoT0GenPaper/internal/pipeline/parser"
	"GoT0GenPaper/internal/pipeline/quality"
	"GoT0GenPaper/internal/pipeline/validator"
	"GoT0GenPaper/internal/storage"
)

// app owns CLI-facing configuration, pipeline state, and generation services.
type app struct {
	ctx context.Context

	cfg        *config.Config
	store      *storage.Store
	storageErr error
	llmC       *llm.Client

	parser    *parser.Parser
	gnnPred   *gnn.StatisticalPredictor
	orch      *orchestrator.Orchestrator
	gen       *generator.Generator
	validator *validator.Validator
	assembler *assembler.Assembler

	// Cross-stage state (persisted to the data dir after each stage).
	lastParse     *parser.ParseResult
	lastBlueprint *orchestrator.Blueprint
	lastSpec      *models.SpecTable
}

// newApp creates the application service, loading config.toml when present.
func newApp() *app {
	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		log.Printf("config load failed, using defaults: %v", err)
		cfg = config.Default()
	}
	a := &app{cfg: cfg, ctx: context.Background()}
	a.rewire()
	return a
}

// rewire (re)creates the LLM client and all pipeline stages from cfg.
// Called during application startup.
func (a *app) rewire() {
	a.cfg.Normalize()
	config.ResolvePaths(a.cfg, filepath.Dir(config.ConfigPath()))
	if err := latex.SetTemplateDir(a.cfg.Paths.Templates); err != nil {
		log.Printf("LaTeX template load failed: %v", err)
	}
	store, err := storage.New(a.cfg.Paths.DataDir, a.cfg.Paths.OutputDir, a.cfg.Paths.Templates)
	a.store = store
	a.storageErr = err
	if err != nil {
		log.Printf("storage init failed: %v", err)
	}
	provider := a.cfg.ActiveProvider()
	if provider == nil {
		log.Printf("no LLM provider configured; running deterministic/offline stages only")
		a.llmC = nil
	} else {
		a.llmC = llm.New(provider.BaseURL, provider.APIKey)
		a.llmC.ReasoningEffort = provider.ReasoningEffort
		a.llmC.MaxTokens = provider.MaxTokens
	}

	a.parser = parser.New(a.llmC)
	a.gnnPred = gnn.New(nil)
	a.orch = orchestrator.New(nil, nil)

	a.gen = generator.New(a.llmC, nil)
	if provider != nil {
		a.gen.ModelByType = provider.ModelByType
		a.gen.DefaultModel = provider.DefaultModel
	}
	// Cross-family fallback for flaky channels (truncation, upstream blips).
	if provider != nil {
		if fb := provider.FallbackModel; fb != "" && fb != a.gen.DefaultModel {
			a.gen.FallbackModel = fb
		}
	}

	a.validator = validator.New(nil)
	a.assembler = assembler.New()
}

// --- Stage [1]: 真题解析 ---

// parseExams runs the real-exam parser on a source path.
// Results are persisted to data/parse_result.json and propagated downstream:
// candidate pool for CP-SAT, dedup baseline for the validator, few-shot
// examples and templates for the generator.
func (a *app) parseExams(source string) (*parser.ParseResult, error) {
	if err := a.storageReady(); err != nil {
		return nil, err
	}
	res, err := a.parser.Parse(a.ctx, source)
	if err != nil {
		return nil, err
	}
	a.lastParse = res
	if err := a.attachParseArtifacts(res); err != nil {
		return nil, err
	}
	return res, nil
}

// attachParseArtifacts propagates parse output to downstream stages + storage.
func (a *app) attachParseArtifacts(res *parser.ParseResult) error {
	if res == nil {
		return nil
	}
	// Validator dedup baseline.
	a.validator.PastExamDB = res.Questions
	// Generator style examples + LaTeX skeletons.
	a.gen.Templates = res.Templates
	fewShot := make(map[string][]string)
	for _, q := range res.Questions {
		if q.Stem == "" {
			continue
		}
		key := q.TemplateKey
		if key == "" {
			key = string(q.Type)
		}
		fewShot[key] = append(fewShot[key], q.Stem)
	}
	a.gen.SetFewShot(fewShot)
	// Persist.
	if a.store != nil {
		if err := a.store.SaveDataJSON("parse_result", res); err != nil {
			return fmt.Errorf("save parse result: %w", err)
		}
	}
	return nil
}

// loadStoredParse reloads the persisted parse result into state.
func (a *app) loadStoredParse() error {
	if err := a.storageReady(); err != nil {
		return err
	}
	var r parser.ParseResult
	if err := a.store.LoadDataJSON("parse_result", &r); err != nil {
		return err
	}
	a.lastParse = &r
	return a.attachParseArtifacts(&r)
}

// --- Stage [2]: GNN 预测 ---

// PredictOutput bundles both GNN predictions.
type PredictOutput struct {
	Weights      map[string]map[string]float64 `json:"weights"`      // 知识点 → (题型_认知层) → 概率
	Cooccurrence map[string]map[string]float64 `json:"cooccurrence"` // 知识点对 Jaccard 共现
	NumPoints    int                           `json:"numPoints"`
}

// predictWeights runs Stage 2 and wires the priors into the orchestrator.
// When qs is empty, the previously parsed questions are used.
func (a *app) predictWeights(qs []models.PastQuestion) (*PredictOutput, error) {
	if err := a.storageReady(); err != nil {
		return nil, err
	}
	if len(qs) == 0 && a.lastParse != nil {
		qs = a.lastParse.Questions
	}
	a.gnnPred = gnn.New(qs)
	weights, err := a.gnnPred.PredictWeights(a.ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("predict weights: %w", err)
	}
	coocc, err := a.gnnPred.PredictCooccurrence(a.ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("predict cooccurrence: %w", err)
	}
	// Wire priors into CP-SAT so composeExam uses them.
	a.orch.GNNWeights = weights
	a.orch.GNNCooccurrence = coocc
	if a.store != nil {
		if err := a.store.SaveDataJSON("gnn_weights", weights); err != nil {
			return nil, fmt.Errorf("save gnn weights: %w", err)
		}
		if err := a.store.SaveDataJSON("gnn_cooccurrence", coocc); err != nil {
			return nil, fmt.Errorf("save gnn cooccurrence: %w", err)
		}
	}
	return &PredictOutput{Weights: weights, Cooccurrence: coocc, NumPoints: len(weights)}, nil
}

// --- Stage [3]: CP-SAT 编排 ---

// composeExam runs CP-SAT to produce the spec table (生成前双向细目表).
// When candidates is empty, the previously parsed questions are used.
// The blueprint and spec table are persisted and kept for validation/repair.
func (a *app) composeExam(bp orchestrator.Blueprint, candidates []models.PastQuestion) (*models.SpecTable, error) {
	if err := a.storageReady(); err != nil {
		return nil, err
	}
	useCachedCandidates := len(candidates) == 0 && a.lastParse != nil
	if useCachedCandidates {
		candidates = a.lastParse.Questions
	}
	if subject, ok := subjectProfile(bp.Subject); ok && (subject.Key == "math2" || subject.Key == "politics") {
		candidates = quality.FilterPastQuestions(subject.Key, candidates)
		if useCachedCandidates {
			a.lastParse.Questions = append([]models.PastQuestion(nil), candidates...)
			if err := a.attachParseArtifacts(a.lastParse); err != nil {
				return nil, fmt.Errorf("stage3 subject filter: %w", err)
			}
		}
	}
	spec, err := a.orch.Compose(a.ctx, &bp, candidates)
	if err != nil {
		return nil, err
	}
	a.lastBlueprint = &bp
	a.lastSpec = spec
	if a.store != nil {
		if err := a.store.SaveDataJSON("blueprint", &bp); err != nil {
			return nil, fmt.Errorf("save blueprint: %w", err)
		}
		if err := a.store.SaveDataJSON("spec_table", spec); err != nil {
			return nil, fmt.Errorf("save spec table: %w", err)
		}
	}
	return spec, nil
}

// --- Stage [4]: 逐题生成 ---

// generateQuestions runs the LLM generator on a spec table.
func (a *app) generateQuestions(spec models.SpecTable) ([]models.GeneratedQuestion, error) {
	if err := a.storageReady(); err != nil {
		return nil, err
	}
	qs, err := a.gen.GenerateAll(a.ctx, spec)
	if err != nil {
		return nil, err
	}
	if a.store != nil {
		if err := a.store.SaveDataJSON("generated_questions", qs); err != nil {
			return nil, fmt.Errorf("save generated questions: %w", err)
		}
	}
	return qs, nil
}

// --- Whole-paper validation ---

// validatePaper runs whole-paper checks (coverage/difficulty/dedup/score).
// When bp is nil, the last composed blueprint is used.
func (a *app) validatePaper(paper models.ExamPaper, bp *orchestrator.Blueprint) (*validator.ValidationResult, error) {
	if bp == nil {
		bp = a.lastBlueprint
	}
	return a.validator.Validate(a.ctx, paper, bp)
}

// RepairOutput reports the outcome of the Stage-6 repair loop.
type RepairOutput struct {
	Paper      models.ExamPaper            `json:"paper"`
	Rounds     int                         `json:"rounds"`
	Validation *validator.ValidationResult `json:"validation"`
}

// repairExamPaper runs the repair protocol: regenerate only violated题位,
// ≤ maxRounds rounds, then degrade (return last state + final validation).
func (a *app) repairExamPaper(paper models.ExamPaper, maxRounds int) (*RepairOutput, error) {
	p, val, rounds, err := a.repairPaper(a.ctx, paper, a.lastBlueprint, maxRounds)
	if err != nil {
		return nil, err
	}
	return &RepairOutput{Paper: p, Rounds: rounds, Validation: val}, nil
}

// repairStoredPaper repairs selected entries in the existing assembled paper.
// It intentionally skips parsing, composition, and regeneration of untouched
// questions so a small fix does not invalidate a whole paper.
func (a *app) repairStoredPaper(specIDs []string) (*RepairOutput, error) {
	if err := a.storageReady(); err != nil {
		return nil, err
	}
	if a.lastParse == nil {
		if err := a.loadStoredParse(); err != nil {
			return nil, fmt.Errorf("读取历年真题语料以进行去重校验: %w", err)
		}
	}
	paper, err := a.loadStoredPaper()
	if err != nil {
		return nil, err
	}
	var bp orchestrator.Blueprint
	if err := a.store.LoadDataJSON("blueprint", &bp); err != nil {
		return nil, fmt.Errorf("读取 blueprint: %w", err)
	}
	a.lastBlueprint = &bp
	if len(specIDs) == 0 {
		val, validateErr := a.validator.Validate(a.ctx, *paper, &bp)
		if validateErr != nil {
			return nil, validateErr
		}
		specIDs = val.ViolatedSpecIDs
	}
	if !a.regenerateSlots(a.ctx, paper, specIDs) {
		return nil, fmt.Errorf("没有成功修复指定题位: %s", strings.Join(specIDs, ", "))
	}
	paper.Questions = orderQuestionsBySpec(paper.Questions, paper.SpecTable.Entries)
	paper.TotalScore = sumPaperQuestionScores(paper.Questions)
	val, err := a.validator.Validate(a.ctx, *paper, &bp)
	if err != nil {
		return nil, err
	}
	if err := a.store.SaveDataJSON("generated_questions", paper.Questions); err != nil {
		return nil, fmt.Errorf("同步 generated_questions: %w", err)
	}
	if _, err := a.assemblePaper(*paper); err != nil {
		return nil, err
	}
	return &RepairOutput{Paper: *paper, Rounds: 1, Validation: val}, nil
}

// --- Stage [7]: 装配 ---

// assemblePaper persists the printable exam and its structured validation data.
func (a *app) assemblePaper(paper models.ExamPaper) (*assembler.AssembleOutput, error) {
	if err := a.storageReady(); err != nil {
		return nil, err
	}
	out, err := a.assembler.Assemble(a.ctx, paper)
	if err != nil {
		return nil, err
	}
	if a.store != nil {
		files := []struct {
			name    string
			content string
		}{
			{"exam.tex", out.ExamLaTeX},
		}
		for _, file := range files {
			if err := a.store.SaveOutputText(file.name, file.content); err != nil {
				return nil, fmt.Errorf("save %s: %w", file.name, err)
			}
		}
		if err := a.store.SaveOutputJSON("paper", paper); err != nil {
			return nil, fmt.Errorf("save paper: %w", err)
		}
	}
	return out, nil
}

// --- PDF compilation ---

// CompileStatus reports the compile outcome of one deliverable.
type CompileStatus struct {
	File        string                `json:"file"`
	OK          bool                  `json:"ok"`
	PDF         string                `json:"pdf"`
	Engine      string                `json:"engine"`
	Duration    string                `json:"duration"`
	Diagnostics []latex.TexDiagnostic `json:"diagnostics,omitempty"`
	Error       string                `json:"error,omitempty"`
}

// compilePaperPDF compiles the assembled exam into a PDF.
func (a *app) compilePaperPDF() ([]CompileStatus, error) {
	if a.store == nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	paperPath := filepath.Join(a.store.OutputDir, "paper.json")
	if _, statErr := os.Stat(paperPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, fmt.Errorf("未找到 paper.json，请先装配试卷")
		}
		return nil, fmt.Errorf("检查 paper.json: %w", statErr)
	}
	var paper models.ExamPaper
	if err := a.store.LoadJSON(a.store.OutputDir, "paper", &paper); err != nil {
		return nil, fmt.Errorf("读取 paper.json: %w", err)
	}
	if err := assembler.ValidateReady(paper); err != nil {
		return nil, err
	}
	engine, err := latex.FindEngine()
	if err != nil {
		return nil, err
	}
	var statuses []CompileStatus
	for _, name := range []string{"exam.tex"} {
		texPath := filepath.Join(a.store.OutputDir, name)
		if _, err := os.Stat(texPath); err != nil {
			statuses = append(statuses, CompileStatus{File: name, Error: "未找到, 请先装配"})
			continue
		}
		res, cerr := latex.CompileTex(a.ctx, engine, texPath)
		st := CompileStatus{File: name}
		if res != nil {
			st.Engine = res.Engine
			st.PDF = res.PDF
			st.Duration = res.Duration
			st.Diagnostics = res.Diagnostics
		}
		if cerr != nil {
			st.Error = cerr.Error()
		} else {
			st.OK = true
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}

// --- Artifacts ---

// listArtifacts returns file names in the data and output directories.
func (a *app) listArtifacts() (map[string][]string, error) {
	out := make(map[string][]string)
	if a.store == nil {
		return out, nil
	}
	if files, err := a.store.ListDir(a.store.DataDir); err == nil {
		out["data"] = files
	}
	if files, err := a.store.ListDir(a.store.OutputDir); err == nil {
		out["output"] = files
	}
	return out, nil
}

// loadStoredPaper loads the last assembled paper from output/paper.json.
func (a *app) loadStoredPaper() (*models.ExamPaper, error) {
	if a.store == nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	var p models.ExamPaper
	if err := a.store.LoadJSON(a.store.OutputDir, "paper", &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ProviderConnectionRequest contains only the values needed for a one-off
// connection check. CLI overrides can be tested without changing the config.
type ProviderConnectionRequest struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

// ConnectionTestResult is safe to print in CLI output: it never
// contains the submitted API key or the full upstream response body.
type ConnectionTestResult struct {
	OK           bool   `json:"ok"`
	ProviderName string `json:"providerName,omitempty"`
	Model        string `json:"model,omitempty"`
	Message      string `json:"message"`
	LatencyMs    int64  `json:"latencyMs"`
}

// testProviderConnection sends a tiny request with a short timeout. A nil
// application context is valid in unit tests and uses Background instead.
func (a *app) testProviderConnection(req ProviderConnectionRequest) (result ConnectionTestResult) {
	return a.testProvider(req)
}

func (a *app) testProvider(req ProviderConnectionRequest) (result ConnectionTestResult) {
	started := time.Now()
	result = ConnectionTestResult{Model: strings.TrimSpace(req.Model)}
	defer func() { result.LatencyMs = time.Since(started).Milliseconds() }()

	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" {
		result.Message = "请填写 Base URL"
		return result
	}
	if result.Model == "" {
		result.Message = "请填写测试模型"
		return result
	}

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	messages := []llm.Message{{Role: "user", Content: "请只回复 OK"}}
	_, err := llm.New(baseURL, req.APIKey).Chat(ctx, result.Model, messages, llm.ChatOptions{MaxTokens: 8, DisableRetries: true})
	if err == nil {
		result.OK = true
		result.Message = "连接成功"
		return result
	}
	result.Message = connectionErrorMessage(err)
	return result
}

func connectionErrorMessage(err error) string {
	if err == nil {
		return "连接成功"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时，请检查地址和网络"
	}
	message := err.Error()
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "model is not available in the current token plan"):
		return "上游账号当前套餐无权调用该模型，请在上游确认模型权限"
	case strings.Contains(lower, "model_price_error") || strings.Contains(lower, "price not configured") || strings.Contains(message, "价格未配置"):
		return "本地网关未配置该模型的计费倍率或价格"
	case strings.Contains(lower, " 401:") || strings.Contains(lower, " 403:"):
		return "鉴权失败，请检查 API Key"
	case strings.Contains(lower, " 404:"):
		return "接口不存在，请确认 Base URL 是 OpenAI 兼容地址"
	case strings.Contains(lower, " 405:"):
		return "接口拒绝 POST 请求，请确认该地址提供 OpenAI 兼容 API；当前地址可能只是网页入口或受区域限制"
	case strings.Contains(lower, " 429:"):
		return "请求被限流，请稍后重试"
	case strings.Contains(lower, " 400:") && strings.Contains(lower, "model"):
		return "模型不可用，请检查模型名称"
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") || strings.Contains(lower, "network is unreachable"):
		return "无法连接服务器，请检查 Base URL 和网络"
	default:
		return "连接失败: " + sanitizeConnectionError(message)
	}
}

func sanitizeConnectionError(message string) string {
	// Keep diagnostics useful without displaying a provider response that may
	// contain request metadata or credentials.
	if len(message) > 180 {
		message = message[:180] + "…"
	}
	return strings.TrimSpace(message)
}

// --- Project management / health ---

func (a *app) storageReady() error {
	if a == nil || a.store == nil {
		return fmt.Errorf("本地存储未初始化")
	}
	if a.storageErr != nil {
		return fmt.Errorf("本地存储未初始化: %w", a.storageErr)
	}
	return nil
}

// HealthStatus reports system readiness.
type HealthStatus struct {
	LLM             bool   `json:"llm"`           // client object exists
	LLMConfigured   bool   `json:"llmConfigured"` // endpoint/model/credential usable
	Storage         bool   `json:"storage"`
	Message         string `json:"message"`
	StoragePath     string `json:"storagePath"`
	StorageError    string `json:"storageError,omitempty"`
	LLMConfigReason string `json:"llmConfigReason,omitempty"`
}

// healthCheck verifies backend readiness.
func (a *app) healthCheck() HealthStatus {
	status := HealthStatus{
		LLM: a != nil && a.llmC != nil,
	}
	if a != nil && a.cfg != nil {
		status.StoragePath = a.cfg.Paths.OutputDir
		if provider := a.cfg.ActiveProvider(); provider != nil {
			status.LLMConfigured = strings.TrimSpace(provider.BaseURL) != "" && strings.TrimSpace(provider.DefaultModel) != ""
			if status.LLMConfigured && strings.TrimSpace(provider.APIKey) == "" && !isLocalProvider(provider.BaseURL) {
				status.LLMConfigured = false
				status.LLMConfigReason = "当前供应商未配置 API Key"
			}
		} else {
			status.LLMConfigReason = "没有可用的当前供应商"
		}
	}
	if a != nil && a.store != nil && a.storageErr == nil {
		status.Storage = true
		status.StoragePath = a.store.OutputDir
	}
	if a != nil && a.storageErr != nil {
		status.StorageError = a.storageErr.Error()
		status.Message = "本地存储初始化失败: " + a.storageErr.Error()
	} else if !status.LLM {
		status.Message = "LLM 客户端未初始化"
	} else if !status.LLMConfigured {
		status.Message = "LLM 客户端已初始化，但供应商配置未就绪"
	} else {
		status.Message = "本地存储和 LLM 供应商配置已就绪；网络连接请运行 test --network"
	}
	return status
}
