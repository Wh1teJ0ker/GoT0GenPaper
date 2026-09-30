package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"GoT0GenPaper/internal/config"
	"GoT0GenPaper/internal/latex"
	"GoT0GenPaper/internal/storage"
)

type SelfTestItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Required bool   `json:"required"`
	Message  string `json:"message"`
}

type SelfTestReport struct {
	OK       bool           `json:"ok"`
	Passed   int            `json:"passed"`
	Warnings int            `json:"warnings"`
	Failed   int            `json:"failed"`
	Config   string         `json:"config"`
	Items    []SelfTestItem `json:"items"`
}

func runSelfTest(args []string) error {
	includeNetwork := false
	for _, arg := range args {
		switch arg {
		case "--network":
			includeNetwork = true
		case "-h", "--help":
			fmt.Println("用法: got0genpaper test [--network]")
			fmt.Println("默认只检查本地依赖；--network 会实际测试当前供应商")
			return nil
		default:
			return fmt.Errorf("test 不支持参数 %q", arg)
		}
	}

	report := SelfTestReport{Config: config.ConfigPath()}
	add := func(id, name, status string, required bool, message string) {
		report.Items = append(report.Items, SelfTestItem{ID: id, Name: name, Status: status, Required: required, Message: message})
		switch status {
		case "pass":
			report.Passed++
		case "warn":
			report.Warnings++
		case "fail":
			report.Failed++
		}
	}

	add("go", "Go 运行时", "pass", true, runtime.Version())
	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		add("config", "TOML 配置", "fail", true, err.Error())
		return finishSelfTest(report)
	}
	add("config", "TOML 配置", "pass", true, "已加载 "+config.ConfigPath())

	provider := cfg.ActiveProvider()
	if provider == nil {
		add("provider", "当前供应商", "fail", true, "没有可用的 active provider")
	} else if !validProviderURL(provider.BaseURL) || strings.TrimSpace(provider.DefaultModel) == "" {
		add("provider", "当前供应商", "fail", true, "Base URL 和默认模型不能为空")
	} else if provider.APIKey == "" && !isLocalProvider(provider.BaseURL) {
		add("provider", "当前供应商", "warn", true, provider.Name+" 未配置 API Key；可通过 TOML 或 NEWAPI_API_KEY 提供")
	} else {
		add("provider", "当前供应商", "pass", true, provider.Name+" / "+provider.DefaultModel)
	}

	if err := latex.SetTemplateDir(cfg.Paths.Templates); err != nil {
		add("templates", "LaTeX 模板", "fail", true, err.Error())
	} else {
		add("templates", "LaTeX 模板", "pass", true, "外部模板已加载并通过完整性校验")
	}

	store, storeErr := storage.New(cfg.Paths.DataDir, cfg.Paths.OutputDir, cfg.Paths.Templates)
	if storeErr != nil {
		add("storage", "本地目录", "fail", true, storeErr.Error())
	} else {
		add("storage", "本地目录", "pass", true, "data、output、templates 可创建")
		checkWritable(add, "output-writable", "输出目录写权限", store.OutputDir)
	}

	for _, subject := range selectableSubjects {
		path := filepath.Join(cfg.Paths.DataDir, subject.Source)
		files, globErr := filepath.Glob(filepath.Join(path, "*.md"))
		if globErr != nil || len(files) == 0 {
			add("exam-"+subject.Key, subject.Label+" 题库", "fail", true, "未找到 Markdown 真题: "+path)
		} else {
			add("exam-"+subject.Key, subject.Label+" 题库", "pass", true, fmt.Sprintf("%d 个文件", len(files)))
		}
	}

	if engine, engineErr := latex.FindEngine(); engineErr != nil {
		add("latex", "PDF 编译引擎", "warn", false, engineErr.Error())
	} else {
		add("latex", "PDF 编译引擎", "pass", false, filepath.Base(engine))
	}
	if includeNetwork && provider != nil {
		a := newCLIAPI()
		result := a.TestProviderConnection(ProviderConnectionRequest{BaseURL: provider.BaseURL, APIKey: provider.APIKey, Model: provider.DefaultModel})
		if result.OK {
			add("provider-network", "供应商连接", "pass", true, fmt.Sprintf("%s，耗时 %d ms", result.Message, result.LatencyMs))
		} else {
			add("provider-network", "供应商连接", "fail", true, result.Message)
		}
		visionModel := provider.VisionModel
		if visionModel == "" {
			visionModel = provider.DefaultModel
		}
		visionResult := a.TestProviderVisionConnection(ProviderConnectionRequest{BaseURL: provider.BaseURL, APIKey: provider.APIKey, Model: visionModel})
		if visionResult.OK {
			add("provider-vision", "多模态供应商连接", "pass", true, fmt.Sprintf("%s，耗时 %d ms", visionResult.Message, visionResult.LatencyMs))
		} else {
			add("provider-vision", "多模态供应商连接", "fail", true, visionResult.Message)
		}
	}

	return finishSelfTest(report)
}

func checkWritable(add func(string, string, string, bool, string), id, name, dir string) {
	file, err := os.CreateTemp(dir, ".got0genpaper-write-test-*")
	if err != nil {
		add(id, name, "fail", true, err.Error())
		return
	}
	path := file.Name()
	closeErr := file.Close()
	removeErr := os.Remove(path)
	if closeErr != nil || removeErr != nil {
		add(id, name, "fail", true, "测试文件清理失败")
		return
	}
	add(id, name, "pass", true, dir)
}

func isLocalProvider(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func validProviderURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

func finishSelfTest(report SelfTestReport) error {
	report.OK = report.Failed == 0
	if err := printJSON(report); err != nil {
		return err
	}
	if !report.OK {
		return fmt.Errorf("系统自检失败: %d 项必需检查未通过", report.Failed)
	}
	return nil
}
