package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathsUsesConfigDirectory(t *testing.T) {
	cfg := Default()
	cfg.Paths = PathsConfig{
		DataDir:   "data",
		OutputDir: "output",
		Templates: "templates",
	}
	base := filepath.Join(t.TempDir(), "settings")
	ResolvePaths(cfg, base)

	for _, got := range []string{cfg.Paths.DataDir, cfg.Paths.OutputDir, cfg.Paths.Templates} {
		if !filepath.IsAbs(got) {
			t.Errorf("path %q is not absolute", got)
		}
		if filepath.Dir(got) != base {
			t.Errorf("path %q is not rooted at config directory %q", got, base)
		}
	}
}

func TestLoadResolvesRelativePathsAndPreservesAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	contents := []byte(`{
  "newapi": {"baseUrl": "https://example.invalid"},
  "paths": {
    "dataDir": "data",
    "outputDir": "output",
    "templates": "/tmp/got0genpaper-templates"
  }
}`)
	if err := os.WriteFile(configPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "data"); cfg.Paths.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.Paths.DataDir, want)
	}
	if want := filepath.Join(root, "output"); cfg.Paths.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q", cfg.Paths.OutputDir, want)
	}
	if cfg.Paths.Templates != "/tmp/got0genpaper-templates" {
		t.Errorf("absolute Templates path changed to %q", cfg.Paths.Templates)
	}
}

func TestLegacyJSONMigratesToTOML(t *testing.T) {
	root := t.TempDir()
	jsonPath := filepath.Join(root, "config.json")
	tomlPath := filepath.Join(root, "config.toml")
	contents := []byte(`{
  "newapi": {
    "baseUrl": "http://127.0.0.1:9/v1",
    "apiKey": "secret-value",
    "modelByType": {"choice": "test-model"},
    "judgeModel": "test-model",
    "defaultModel": "test-model"
  },
  "paths": {"dataDir": "data", "outputDir": "output", "templates": "templates"}
}`)
	if err := os.WriteFile(jsonPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveProvider() == nil || cfg.ActiveProvider().APIKey != "secret-value" {
		t.Fatalf("legacy provider was not migrated: %+v", cfg.ActiveProvider())
	}
	if _, err := os.Stat(tomlPath); err != nil {
		t.Fatalf("migrated TOML missing: %v", err)
	}
	info, err := os.Stat(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("TOML permissions = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || string(data)[:1] == "{" {
		t.Errorf("migrated file is not TOML: %q", data)
	}
}

func TestTOMLRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := Default()
	cfg.Providers.Providers = append(cfg.Providers.Providers, ProviderConfig{
		ID: "ollama", Name: "Ollama", BaseURL: "http://127.0.0.1:11434/v1", DefaultModel: "llama3.1", FallbackModel: "llama3.2", Enabled: true,
		ModelByType: map[string]string{"choice": "llama3.1"}, ReasoningEffort: "low", MaxTokens: 2048,
	})
	cfg.Providers.ActiveID = "ollama"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Providers.ActiveID != "ollama" || loaded.ActiveProvider().DefaultModel != "llama3.1" {
		t.Fatalf("TOML round trip lost active provider: %+v", loaded.Providers)
	}
	if loaded.ActiveProvider().ReasoningEffort != "low" || loaded.ActiveProvider().MaxTokens != 2048 {
		t.Fatalf("TOML round trip lost LLM generation controls: %+v", loaded.ActiveProvider())
	}
	if loaded.ActiveProvider().FallbackModel != "llama3.2" {
		t.Fatalf("fallback model = %q, want llama3.2", loaded.ActiveProvider().FallbackModel)
	}
}

func TestLegacyJudgeModelMigratesToGenerationFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := []byte(`[providers]
active_id = "legacy"

[[providers.providers]]
id = "legacy"
name = "Legacy"
base_url = "http://127.0.0.1:1234"
default_model = "primary"
judge_model = "backup"
enabled = true
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ActiveProvider().FallbackModel; got != "backup" {
		t.Fatalf("fallback model = %q, want legacy model backup", got)
	}
	if got := cfg.ActiveProvider().JudgeModel; got != "" {
		t.Fatalf("legacy judge field should be normalized away, got %q", got)
	}
}

func TestNormalizeDoesNotSelectDisabledActiveProvider(t *testing.T) {
	cfg := Default()
	cfg.Providers.Providers = []ProviderConfig{
		{ID: "disabled", Name: "Disabled", Enabled: false},
		{ID: "enabled", Name: "Enabled", Enabled: true},
	}
	cfg.Providers.ActiveID = "disabled"
	cfg.Normalize()
	if cfg.Providers.ActiveID != "enabled" {
		t.Fatalf("active provider = %q, want enabled", cfg.Providers.ActiveID)
	}
}
