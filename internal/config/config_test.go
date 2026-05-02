package config

import (
	"os"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Planner.ModelRef == "" {
		t.Error("planner model_ref should have a default")
	}
	if cfg.Validator.ModelRef == "" {
		t.Error("validator model_ref should have a default")
	}
	if cfg.Retry.PlannerAttempts < 1 {
		t.Error("planner attempts should be at least 1")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("nonexistent.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("ORQESTRA_VALIDATOR_MODEL_REF", "custom-ref")

	content := `
providers:
  local:
    base_url: http://localhost
    type: openai
models:
  custom-ref:
    provider: local
    model: custom-model
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
	if cfg.Validator.ModelRef != "custom-ref" {
		t.Errorf("expected env override, got %q", cfg.Validator.ModelRef)
	}
}

func TestLoad_ValidYAML(t *testing.T) {
	content := `
providers:
  local:
    base_url: http://test:1234
    type: openai
models:
  test-planner:
    provider: local
    model: test-model
  test-validator:
    provider: local
    model: test-val
planner:
  model_ref: test-planner
validator:
  model_ref: test-validator
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
	if cfg.Planner.ModelRef != "test-planner" {
		t.Errorf("expected test-planner, got %q", cfg.Planner.ModelRef)
	}
	if cfg.Validator.ModelRef != "test-validator" {
		t.Errorf("expected test-validator, got %q", cfg.Validator.ModelRef)
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

func TestRuntimeOptions(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {BaseURL: "http://localhost", Type: "openai"},
		},
		Models: map[string]ModelConfig{
			"fast": {Provider: "local", Model: "qwen36"},
			"worker": {
				Provider: "local",
				Model:    "qwen36",
				SmallRef: "fast",
				Binary:   "claude-test",
			},
		},
	}

	runtime, err := cfg.RuntimeOptions("worker")
	if err != nil {
		t.Fatalf("RuntimeOptions() error: %v", err)
	}
	if runtime.SmallRef != "fast" {
		t.Errorf("SmallRef = %q, want fast", runtime.SmallRef)
	}
	if runtime.Binary != "claude-test" {
		t.Errorf("Binary = %q, want claude-test", runtime.Binary)
	}
}

func TestLoad_ValidationAtLoadTime_InvalidSmallRef(t *testing.T) {
	content := `
providers:
  local:
    base_url: http://localhost
    type: openai
models:
  worker:
    provider: local
    model: qwen36
    small_ref: missing
`
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	_, err = Load(f.Name())
	if err == nil {
		t.Fatal("expected validation error for missing small_ref")
	}
}

func TestLoad_ValidationAtLoadTime_InvalidGraphModelRef(t *testing.T) {
	content := `
providers:
  local:
    base_url: http://localhost
    type: openai
models:
  qwen:
    provider: local
    model: qwen36
execution_graph:
  agents:
    - id: implement
      role: implementer
      model_ref: missing
`
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	_, err = Load(f.Name())
	if err == nil {
		t.Fatal("expected validation error for missing graph model_ref")
	}
}

func TestParseTokenLimit(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"unlimited", TokenLimitUnlimited, false},
		{"UNLIMITED", TokenLimitUnlimited, false},
		{"UnLiMiTeD", TokenLimitUnlimited, false},
		{"300K", 300_000, false},
		{"300k", 300_000, false},
		{"1M", 1_000_000, false},
		{"1m", 1_000_000, false},
		{"1.5M", 1_500_000, false},
		{"2.5K", 2_500, false},
		{"500000", 500_000, false},
		{"100", 100, false},
		{"0.5M", 500_000, false},
		// Errors
		{"abc", 0, true},
		{"0K", 0, true},
		{"-1K", 0, true},
		{"-500", 0, true},
		{"0", 0, true},
		{"K", 0, true},
		{"M", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseTokenLimit(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTokenLimit(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseTokenLimit(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolvedTokenLimits(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {BaseURL: "http://localhost", Type: "openai"},
		},
		Models: map[string]ModelConfig{
			"fast":      {Provider: "local", Model: "qwen36", TokenLimit: "1M"},
			"worker":    {Provider: "local", Model: "qwen36", TokenLimit: "1M"},
			"planner":   {Provider: "local", Model: "opus", TokenLimit: "300K"},
			"unlimited": {Provider: "local", Model: "cheap", TokenLimit: "unlimited"},
			"nobudget":  {Provider: "local", Model: "other"},
		},
	}

	limits, err := cfg.ResolvedTokenLimits()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limits["qwen36"] != 1_000_000 {
		t.Errorf("qwen36 limit = %d, want 1000000", limits["qwen36"])
	}
	if limits["opus"] != 300_000 {
		t.Errorf("opus limit = %d, want 300000", limits["opus"])
	}
	if _, ok := limits["cheap"]; ok {
		t.Error("unlimited model should not appear in limits")
	}
	if _, ok := limits["other"]; ok {
		t.Error("unconfigured model should not appear in limits")
	}
}

func TestResolvedTokenLimits_Conflict(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {BaseURL: "http://localhost", Type: "openai"},
		},
		Models: map[string]ModelConfig{
			"a": {Provider: "local", Model: "qwen36", TokenLimit: "1M"},
			"b": {Provider: "local", Model: "qwen36", TokenLimit: "500K"},
		},
	}

	_, err := cfg.ResolvedTokenLimits()
	if err == nil {
		t.Fatal("expected error for conflicting limits on same underlying model")
	}
}

func TestLoad_ValidationAtLoadTime_InvalidTokenLimit(t *testing.T) {
	content := `
providers:
  local:
    base_url: http://localhost
    type: openai
models:
  bad:
    provider: local
    model: qwen36
    token_limit: "garbage"
`
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	_, err = Load(f.Name())
	if err == nil {
		t.Fatal("expected validation error for invalid token_limit")
	}
}
