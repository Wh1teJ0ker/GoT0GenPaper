// Package config loads the TOML runtime configuration and manages providers.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const appName = "GoT0GenPaper"

type Config struct {
	// NewAPI is retained only as an in-memory compatibility view for callers
	// that still use the old single-provider fields. TOML never writes it.
	NewAPI    NewAPIConfig    `json:"newapi,omitempty" toml:"-"`
	Providers ProvidersConfig `json:"providers" toml:"providers"`
	Paths     PathsConfig     `json:"paths" toml:"paths"`
}

type NewAPIConfig struct {
	BaseURL      string            `json:"baseUrl" toml:"base_url"`
	APIKey       string            `json:"apiKey" toml:"api_key"`
	ModelByType  map[string]string `json:"modelByType" toml:"model_by_type"`
	JudgeModel   string            `json:"judgeModel" toml:"judge_model"`
	VisionModel  string            `json:"visionModel" toml:"vision_model"`
	DefaultModel string            `json:"defaultModel" toml:"default_model"`
}

type ProviderConfig struct {
	ID              string            `json:"id" toml:"id"`
	Name            string            `json:"name" toml:"name"`
	BaseURL         string            `json:"baseUrl" toml:"base_url"`
	APIKey          string            `json:"apiKey" toml:"api_key"`
	ModelByType     map[string]string `json:"modelByType" toml:"model_by_type"`
	JudgeModel      string            `json:"judgeModel" toml:"judge_model"`
	VisionModel     string            `json:"visionModel" toml:"vision_model"`
	DefaultModel    string            `json:"defaultModel" toml:"default_model"`
	ReasoningEffort string            `json:"reasoningEffort,omitempty" toml:"reasoning_effort,omitempty"`
	MaxTokens       int               `json:"maxTokens,omitempty" toml:"max_tokens,omitempty"`
	Enabled         bool              `json:"enabled" toml:"enabled"`
}

type ProvidersConfig struct {
	ActiveID  string           `json:"activeId" toml:"active_id"`
	Providers []ProviderConfig `json:"providers" toml:"providers"`
}

type PathsConfig struct {
	DataDir   string `json:"dataDir" toml:"data_dir"`
	OutputDir string `json:"outputDir" toml:"output_dir"`
	Templates string `json:"templates" toml:"templates"`
}

func Default() *Config {
	dataDir := filepath.Join(AppDataDir(), "data")
	outputDir := filepath.Join(AppDataDir(), "output")
	templatesDir := filepath.Join(AppDataDir(), "templates")
	provider := ProviderConfig{
		ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com", Enabled: true,
		ModelByType: map[string]string{"choice": "deepseek-v4.1-flash", "fill_blank": "deepseek-v4.1-flash", "major": "deepseek-v4.1-flash"},
		JudgeModel:  "deepseek-v4.1-flash", DefaultModel: "deepseek-v4.1-flash",
		VisionModel: "deepseek-v4.1-flash",
	}
	cfg := &Config{
		Providers: ProvidersConfig{ActiveID: provider.ID, Providers: []ProviderConfig{provider}},
		Paths:     PathsConfig{DataDir: dataDir, OutputDir: outputDir, Templates: templatesDir},
	}
	cfg.Normalize()
	return cfg
}

// Load reads TOML. A legacy JSON sibling is imported once when TOML is absent.
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if legacyPath := legacyJSONPath(path); legacyPath != "" {
			if legacyData, legacyErr := os.ReadFile(legacyPath); legacyErr == nil {
				if err := decodeLegacyJSON(legacyData, cfg); err != nil {
					return nil, err
				}
				cfg.Normalize()
				ResolvePaths(cfg, filepath.Dir(legacyPath))
				if err := cfg.Save(path); err != nil {
					return nil, fmt.Errorf("migrate %s to TOML: %w", legacyPath, err)
				}
				applyEnvOverrides(cfg)
				return cfg, nil
			}
		}
		applyEnvOverrides(cfg)
		return cfg, nil
	}

	if strings.EqualFold(filepath.Ext(path), ".json") {
		if err := decodeLegacyJSON(data, cfg); err != nil {
			return nil, err
		}
		cfg.Normalize()
		ResolvePaths(cfg, filepath.Dir(path))
		if err := cfg.Save(strings.TrimSuffix(path, filepath.Ext(path)) + ".toml"); err != nil {
			return nil, fmt.Errorf("migrate %s to TOML: %w", path, err)
		}
	} else {
		if _, err := toml.Decode(string(data), cfg); err != nil {
			return nil, fmt.Errorf("decode TOML %s: %w", path, err)
		}
		cfg.Normalize()
		ResolvePaths(cfg, filepath.Dir(path))
	}
	applyEnvOverrides(cfg)
	return cfg, nil
}

func legacyJSONPath(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".toml") {
		return strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
	}
	return ""
}

func decodeLegacyJSON(data []byte, cfg *Config) error {
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("decode legacy JSON: %w", err)
	}
	var envelope struct {
		Providers json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode legacy JSON envelope: %w", err)
	}
	if len(envelope.Providers) == 0 || string(envelope.Providers) == "null" {
		cfg.Providers = ProvidersConfig{ActiveID: "default", Providers: []ProviderConfig{providerFromLegacy(cfg.NewAPI)}}
	}
	return nil
}

func AppDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
			base = filepath.Join(home, ".config")
		} else {
			base = "."
		}
	}
	return filepath.Join(base, appName)
}

func ResolvePaths(cfg *Config, baseDir string) {
	if cfg == nil {
		return
	}
	if baseDir == "" {
		baseDir = AppDataDir()
	}
	if !filepath.IsAbs(cfg.Paths.DataDir) {
		cfg.Paths.DataDir = filepath.Join(baseDir, cfg.Paths.DataDir)
	}
	if !filepath.IsAbs(cfg.Paths.OutputDir) {
		cfg.Paths.OutputDir = filepath.Join(baseDir, cfg.Paths.OutputDir)
	}
	if !filepath.IsAbs(cfg.Paths.Templates) {
		cfg.Paths.Templates = filepath.Join(baseDir, cfg.Paths.Templates)
	}
}

func applyEnvOverrides(cfg *Config) {
	cfg.Normalize()
	p := cfg.ActiveProvider()
	if p == nil {
		return
	}
	if value := os.Getenv("NEWAPI_API_KEY"); value != "" {
		p.APIKey = value
	}
	if value := os.Getenv("NEWAPI_BASE_URL"); value != "" {
		p.BaseURL = value
	}
	if value := os.Getenv("NEWAPI_MODEL_CHOICE"); value != "" {
		p.ModelByType["choice"] = value
	}
	if value := os.Getenv("NEWAPI_MODEL_FILL_BLANK"); value != "" {
		p.ModelByType["fill_blank"] = value
	}
	if value := os.Getenv("NEWAPI_MODEL_MAJOR"); value != "" {
		p.ModelByType["major"] = value
	}
	if value := os.Getenv("NEWAPI_MODEL_JUDGE"); value != "" {
		p.JudgeModel = value
	}
	if value := os.Getenv("NEWAPI_MODEL_VISION"); value != "" {
		p.VisionModel = value
	}
	if value := os.Getenv("NEWAPI_MODEL_DEFAULT"); value != "" {
		p.DefaultModel = value
	}
	cfg.SyncLegacy()
}

func (c *Config) Normalize() {
	if c == nil {
		return
	}
	if len(c.Providers.Providers) == 0 {
		c.Providers = ProvidersConfig{ActiveID: "default", Providers: []ProviderConfig{providerFromLegacy(c.NewAPI)}}
	}
	for i := range c.Providers.Providers {
		p := &c.Providers.Providers[i]
		if p.ID == "" {
			p.ID = fmt.Sprintf("provider-%d", i+1)
		}
		if p.Name == "" {
			p.Name = p.ID
		}
		if p.ModelByType == nil {
			p.ModelByType = map[string]string{}
		}
	}
	active := c.ActiveProvider()
	if c.Providers.ActiveID == "" || active == nil || !active.Enabled {
		c.Providers.ActiveID = c.Providers.Providers[0].ID
		for _, p := range c.Providers.Providers {
			if p.Enabled {
				c.Providers.ActiveID = p.ID
				break
			}
		}
	}
	c.SyncLegacy()
}

func (c *Config) ActiveProvider() *ProviderConfig {
	if c == nil {
		return nil
	}
	return c.ProviderByID(c.Providers.ActiveID)
}

func (c *Config) ProviderByID(id string) *ProviderConfig {
	if c == nil {
		return nil
	}
	for i := range c.Providers.Providers {
		if c.Providers.Providers[i].ID == id {
			return &c.Providers.Providers[i]
		}
	}
	return nil
}

func (c *Config) SyncLegacy() {
	p := c.ActiveProvider()
	if p == nil {
		return
	}
	c.NewAPI = NewAPIConfig{BaseURL: p.BaseURL, APIKey: p.APIKey, ModelByType: cloneModels(p.ModelByType), JudgeModel: p.JudgeModel, VisionModel: p.VisionModel, DefaultModel: p.DefaultModel}
}

func providerFromLegacy(legacy NewAPIConfig) ProviderConfig {
	return ProviderConfig{ID: "default", Name: "默认供应商", BaseURL: legacy.BaseURL, APIKey: legacy.APIKey, ModelByType: cloneModels(legacy.ModelByType), JudgeModel: legacy.JudgeModel, VisionModel: legacy.VisionModel, DefaultModel: legacy.DefaultModel, Enabled: true}
}

func cloneModels(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func ConfigPath() string {
	if path := os.Getenv("GOT0GENPAPER_CONFIG"); path != "" {
		if strings.EqualFold(filepath.Ext(path), ".json") {
			path = strings.TrimSuffix(path, filepath.Ext(path)) + ".toml"
		}
		return absolutePath(path)
	}
	if _, err := os.Stat("config.toml"); err == nil {
		return absolutePath("config.toml")
	}
	if _, err := os.Stat("config.json"); err == nil {
		return absolutePath("config.toml")
	}
	return filepath.Join(AppDataDir(), "config.toml")
}

func absolutePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// Save writes TOML with owner-only permissions because it may contain keys.
func (c *Config) Save(path string) error {
	c.Normalize()
	if strings.EqualFold(filepath.Ext(path), ".json") {
		path = strings.TrimSuffix(path, filepath.Ext(path)) + ".toml"
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("encode TOML: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}
