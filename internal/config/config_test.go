package config

import (
	"os"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Planner.Model == "" {
		t.Error("planner model should have a default")
	}
	if cfg.Validator.BaseURL == "" {
		t.Error("validator base URL should have a default")
	}
	if cfg.Retry.PlannerAttempts < 1 {
		t.Error("planner attempts should be at least 1")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load("nonexistent.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg.Planner.Model == "" {
		t.Error("should return defaults when file is missing")
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("ORQESTRA_VALIDATOR_URL", "http://custom:9999")
	t.Setenv("ORQESTRA_VALIDATOR_MODEL", "custom-model")

	cfg, err := Load("nonexistent.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validator.BaseURL != "http://custom:9999" {
		t.Errorf("expected env override, got %q", cfg.Validator.BaseURL)
	}
	if cfg.Validator.Model != "custom-model" {
		t.Errorf("expected model override, got %q", cfg.Validator.Model)
	}
}

func TestLoad_ValidYAML(t *testing.T) {
	content := `
planner:
  model: test-model
validator:
  base_url: http://test:1234
  model: test-validator
`
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Planner.Model != "test-model" {
		t.Errorf("expected test-model, got %q", cfg.Planner.Model)
	}
	if cfg.Validator.BaseURL != "http://test:1234" {
		t.Errorf("expected test URL, got %q", cfg.Validator.BaseURL)
	}
}

func TestResolveModel_Success(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"my-provider": {
				BaseURL: "http://localhost:4141",
				APIKey:  "secret",
				Type:    "anthropic",
			},
		},
		Models: map[string]ModelConfig{
			"my-model": {
				Provider: "my-provider",
				Model:    "claude-sonnet-4.6",
			},
		},
	}

	resolved, err := cfg.ResolveModel("my-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.BaseURL != "http://localhost:4141" {
		t.Errorf("BaseURL = %q, want %q", resolved.BaseURL, "http://localhost:4141")
	}
	if resolved.APIKey != "secret" {
		t.Errorf("APIKey = %q, want %q", resolved.APIKey, "secret")
	}
	if resolved.Model != "claude-sonnet-4.6" {
		t.Errorf("Model = %q, want %q", resolved.Model, "claude-sonnet-4.6")
	}
	if resolved.Type != "anthropic" {
		t.Errorf("Type = %q, want %q", resolved.Type, "anthropic")
	}
}

func TestResolveModel_MissingModel(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{},
		Models:    map[string]ModelConfig{},
	}

	_, err := cfg.ResolveModel("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestResolveModel_MissingProvider(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{},
		Models: map[string]ModelConfig{
			"my-model": {Provider: "no-such-provider", Model: "x"},
		},
	}

	_, err := cfg.ResolveModel("my-model")
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestResolveModel_EnvVarInterpolation(t *testing.T) {
	t.Setenv("TEST_API_KEY", "from-env")

	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"prov": {
				BaseURL: "http://localhost",
				APIKey:  "${TEST_API_KEY}",
				Type:    "openai",
			},
		},
		Models: map[string]ModelConfig{
			"m": {Provider: "prov", Model: "gpt-4"},
		},
	}

	resolved, err := cfg.ResolveModel("m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.APIKey != "from-env" {
		t.Errorf("APIKey = %q, want %q", resolved.APIKey, "from-env")
	}
}

func TestLoad_ValidationAtLoadTime_InvalidProviderRef(t *testing.T) {
	content := `
providers:
  good:
    base_url: http://localhost
    type: anthropic
models:
  bad-model:
    provider: nonexistent
    model: x
`
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	_, err = Load(f.Name())
	if err == nil {
		t.Fatal("expected validation error for model referencing nonexistent provider")
	}
}

func TestLoad_ValidationAtLoadTime_ValidConfig(t *testing.T) {
	content := `
providers:
  local:
    base_url: http://localhost
    type: openai
models:
  my-model:
    provider: local
    model: qwen36
`
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(cfg.Models))
	}
}
