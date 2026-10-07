package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
)

const cliUsage = `GoT0GenPaper 命令行工具

用法:
  got0genpaper version
  got0genpaper generate --subject 408 [--difficulty medium] [--source PATH]
  got0genpaper validate [--paper PATH] [--data DIR]
  got0genpaper reassemble [--paper PATH] [--compile]
  got0genpaper repair [--paper PATH] [--data DIR] [--specs spec_001,spec_002] [--compile]
  got0genpaper test-provider [--provider ID] [--base-url URL] [--model MODEL]
  got0genpaper test [--network]
  got0genpaper health | artifacts | compile
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
	case "validate":
		return runValidate(args[1:])
	case "reassemble":
		return runReassemble(args[1:])
	case "repair":
		return runRepair(args[1:])
	case "compile":
		return runCompile()
	default:
		return fmt.Errorf("未知命令 %q，运行 got0genpaper help 查看用法", args[0])
	}
}

func runHealth() error {
	return printJSON(newCLIApp().healthCheck())
}

func runArtifacts() error {
	artifacts, err := newCLIApp().listArtifacts()
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := newCLIApp()
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
	result := a.testProviderConnection(req)
	result.ProviderName = provider.Name
	if err := printJSON(result); err != nil {
		return err
	}
	if !result.OK {
		return errors.New(result.Message)
	}
	return nil
}

func printJSON(value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
