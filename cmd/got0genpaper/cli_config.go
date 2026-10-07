package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"GoT0GenPaper/internal/config"
)

func runConfigCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("config 需要 list、show、use、add 或 remove")
	}
	a := newCLIApp()
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
	cfg.Providers.Providers = append(cfg.Providers.Providers, config.ProviderConfig{ID: *id, Name: firstNonEmpty(*name, *id), BaseURL: *baseURL, APIKey: *apiKey, ModelByType: map[string]string{"choice": *model, "fill_blank": *model, "major": *model}, DefaultModel: *model, Enabled: true})
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
		Enabled      bool   `json:"enabled"`
		Active       bool   `json:"active"`
	}
	views := make([]view, 0, len(cfg.Providers.Providers))
	for _, p := range cfg.Providers.Providers {
		views = append(views, view{p.ID, p.Name, p.BaseURL, p.DefaultModel, p.Enabled, p.ID == cfg.Providers.ActiveID})
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

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
