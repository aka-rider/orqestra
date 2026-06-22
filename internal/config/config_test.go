package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Contract: config.go validate() — every agent role must have a non-empty model reference
func TestValidate_MissingRoleModels(t *testing.T) {
	// INV-P5-FAILCLOSED: missing role model ref is rejected at validation time
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"missing researcher model", func(c *Config) { c.Researcher.Model = "" }, "researcher"},
		{"missing architect model", func(c *Config) { c.Architect.Model = "" }, "architect"},
		{"missing worker model", func(c *Config) { c.Worker.Model = "" }, "worker"},
		{"missing critic model", func(c *Config) { c.Critic.Model = "" }, "critic"},
		{"missing integrator model", func(c *Config) { c.Integrator.Model = "" }, "integrator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(cfg)
			err := cfg.validate()
			if err == nil {
				t.Fatalf("validate() returned nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("validate() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	// INV-P5-FAILCLOSED: missing config file must error, not silently use defaults
	_, err := Load("nonexistent.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestResolveModel_Success(t *testing.T) {
	// INV-P5-ROUTE: resolved model carries correct BaseURL and type for subprocess env building
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
				Model:    "claude-medium",
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
	if resolved.Model != "claude-medium" {
		t.Errorf("Model = %q, want %q", resolved.Model, "claude-medium")
	}
	if resolved.Type != "anthropic" {
		t.Errorf("Type = %q, want %q", resolved.Type, "anthropic")
	}
}

func TestResolveModel_MissingModel(t *testing.T) {
	// INV-P5-FAILCLOSED: referencing an unknown model must produce *ModelNotFoundError
	cfg := &Config{
		Providers: map[string]ProviderConfig{},
		Models:    map[string]ModelConfig{},
	}

	_, err := cfg.ResolveModel("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	var notFound *ModelNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *ModelNotFoundError, got %T", err)
	}
	if notFound.Name != "nonexistent" {
		t.Errorf("Name = %q, want %q", notFound.Name, "nonexistent")
	}
}

func TestResolveModel_MissingProvider(t *testing.T) {
	// INV-P5-FAILCLOSED: model referencing a nonexistent provider must error
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

func TestLoad_ValidationAtLoadTime_InvalidProviderRef(t *testing.T) {
	// INV-P5-FAILCLOSED: model referencing nonexistent provider fails at Load, not at runtime
	content := `
providers:
  good:
    base_url: http://localhost
    type: anthropic
models:
  medium:
    provider: good
    model: big
  small:
    provider: good
    model: small
  bad-model:
    provider: nonexistent
    model: x
researcher:
  model: medium
architect:
  model: medium
worker:
  model: medium
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
	// INV-P5-ROUTE: a valid config with correct provider+model wiring loads without error
	content := `
providers:
  local:
    base_url: http://localhost
    type: openai
models:
  medium:
    provider: local
    model: big
  small:
    provider: local
    model: small
researcher:
  model: medium
architect:
  model: medium
worker:
  model: medium
`
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	_, err = Load(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
		// INV-P5-FAILCLOSED: malformed token limits must error
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

func TestResolvedTokenLimits_Conflict(t *testing.T) {
	// INV-P5-FAILCLOSED: conflicting token limits on the same underlying model must error
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
	// INV-P5-FAILCLOSED: invalid token_limit format detected at load time
	content := `
providers:
  local:
    base_url: http://localhost
    type: openai
models:
  medium:
    provider: local
    model: big
  small:
    provider: local
    model: small
  bad:
    provider: local
    model: qwen36
    token_limit: "garbage"
researcher:
  model: medium
architect:
  model: medium
worker:
  model: medium
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

func TestValidate_ProviderType(t *testing.T) {
	// INV-P5-FAILCLOSED + INV-P5-ROUTE: invalid/unknown provider types error; valid types accepted
	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "empty provider type",
			config: &Config{
				Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Architect:  ArchitectConfig{BaseAgentConfig: BaseAgentConfig{Model: "large"}},
				Worker:     WorkerConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Critic:      CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator:  IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Providers: map[string]ProviderConfig{
					"local": {BaseURL: "http://localhost:11434", Type: ""},
				},
				Models: map[string]ModelConfig{
					"large":  {Provider: "local", Model: "qwen36"},
					"medium": {Provider: "local", Model: "qwen36"},
					"small":  {Provider: "local", Model: "qwen36"},
				},
			},
			wantErr: `provider "local": type is required`,
		},
		{
			name: "unknown provider type",
			config: &Config{
				Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Architect:  ArchitectConfig{BaseAgentConfig: BaseAgentConfig{Model: "large"}},
				Worker:     WorkerConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Critic:      CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator:  IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Providers: map[string]ProviderConfig{
					"local": {BaseURL: "http://localhost:11434", Type: "copilot-proxy"},
				},
				Models: map[string]ModelConfig{
					"large":  {Provider: "local", Model: "qwen36"},
					"medium": {Provider: "local", Model: "qwen36"},
					"small":  {Provider: "local", Model: "qwen36"},
				},
			},
			wantErr: `provider "local": unknown type "copilot-proxy"`,
		},
		{
			name: "native type with base_url",
			config: &Config{
				Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Architect:  ArchitectConfig{BaseAgentConfig: BaseAgentConfig{Model: "large"}},
				Worker:     WorkerConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Critic:      CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator:  IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Providers: map[string]ProviderConfig{
					"proxy": {BaseURL: "http://127.0.0.1:4141", Type: ProviderTypeNative},
				},
				Models: map[string]ModelConfig{
					"large":  {Provider: "proxy", Model: "claude-opus"},
					"medium": {Provider: "proxy", Model: "claude-sonnet"},
					"small":  {Provider: "proxy", Model: "claude-haiku"},
				},
			},
			wantErr: `provider "proxy": type "native" must not have base_url`,
		},
		{
			name: "anthropic type without base_url",
			config: &Config{
				Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Architect:  ArchitectConfig{BaseAgentConfig: BaseAgentConfig{Model: "large"}},
				Worker:     WorkerConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Critic:      CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator:  IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Providers: map[string]ProviderConfig{
					"local": {Type: ProviderTypeAnthropic},
				},
				Models: map[string]ModelConfig{
					"large":  {Provider: "local", Model: "qwen36"},
					"medium": {Provider: "local", Model: "qwen36"},
					"small":  {Provider: "local", Model: "qwen36"},
				},
			},
			wantErr: `provider "local": type "anthropic" requires base_url`,
		},
		{
			name: "openai type without base_url",
			config: &Config{
				Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Architect:  ArchitectConfig{BaseAgentConfig: BaseAgentConfig{Model: "large"}},
				Worker:     WorkerConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Critic:      CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator:  IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Providers: map[string]ProviderConfig{
					"local": {Type: ProviderTypeOpenAI},
				},
				Models: map[string]ModelConfig{
					"large":  {Provider: "local", Model: "qwen36"},
					"medium": {Provider: "local", Model: "qwen36"},
					"small":  {Provider: "local", Model: "qwen36"},
				},
			},
			wantErr: `provider "local": type "openai" requires base_url`,
		},
		{
			name: "valid native provider without base_url",
			config: &Config{
				Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Architect:  ArchitectConfig{BaseAgentConfig: BaseAgentConfig{Model: "large"}},
				Worker:     WorkerConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Critic:      CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator:  IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Providers: map[string]ProviderConfig{
					"anthropic-native": {Type: ProviderTypeNative},
				},
				Models: map[string]ModelConfig{
					"large":  {Provider: "anthropic-native", Model: "claude-opus-4"},
					"medium": {Provider: "anthropic-native", Model: "claude-sonnet-4"},
					"small":  {Provider: "anthropic-native", Model: "claude-haiku"},
				},
			},
			wantErr: "",
		},
		{
			name: "valid openai provider",
			config: &Config{
				Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Architect:  ArchitectConfig{BaseAgentConfig: BaseAgentConfig{Model: "large"}},
				Worker:     WorkerConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Critic:      CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator:  IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Providers: map[string]ProviderConfig{
					"local": {BaseURL: "http://192.168.50.212:11434", Type: ProviderTypeOpenAI},
				},
				Models: map[string]ModelConfig{
					"large":  {Provider: "local", Model: "qwen36"},
					"medium": {Provider: "local", Model: "qwen36"},
					"small":  {Provider: "local", Model: "qwen36"},
				},
			},
			wantErr: "",
		},
		{
			name: "valid anthropic provider",
			config: &Config{
				Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Architect:  ArchitectConfig{BaseAgentConfig: BaseAgentConfig{Model: "large"}},
				Worker:     WorkerConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Critic:      CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator:  IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Providers: map[string]ProviderConfig{
					"local": {BaseURL: "http://localhost:4141", Type: ProviderTypeAnthropic},
				},
				Models: map[string]ModelConfig{
					"large":  {Provider: "local", Model: "claude-sonnet"},
					"medium": {Provider: "local", Model: "claude-sonnet"},
					"small":  {Provider: "local", Model: "claude-haiku"},
				},
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
