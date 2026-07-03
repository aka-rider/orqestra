package config

import (
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
				Critic:     CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator: IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
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
				Critic:     CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator: IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
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
				Critic:     CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator: IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
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
				Critic:     CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator: IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
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
				Critic:     CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator: IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
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
				Critic:     CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator: IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
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
				Critic:     CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator: IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
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
				Critic:     CriticConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
				Integrator: IntegratorConfig{BaseAgentConfig: BaseAgentConfig{Model: "medium"}},
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

func TestApplyDefaults_SilenceGuardMaxNudges(t *testing.T) {
	// The silence guard must be safe-by-default everywhere (bounded, not an
	// infinite nudge loop) — applyDefaults gives every role a MaxNudges of 3
	// unless the config file overrides it, mirroring the LoopGuard defaults.
	cfg := DefaultConfig()
	roles := map[string]int{
		"researcher": cfg.Researcher.SilenceGuard.MaxNudges,
		"architect":  cfg.Architect.SilenceGuard.MaxNudges,
		"critic":     cfg.Critic.SilenceGuard.MaxNudges,
		"worker":     cfg.Worker.SilenceGuard.MaxNudges,
		"integrator": cfg.Integrator.SilenceGuard.MaxNudges,
	}
	for role, got := range roles {
		if got != 3 {
			t.Errorf("%s: SilenceGuard.MaxNudges = %d, want default 3", role, got)
		}
	}
}

func TestApplyDefaults_SilenceGuardMaxNudgesPreservesExplicitValue(t *testing.T) {
	cfg := &Config{}
	cfg.Architect.SilenceGuard.MaxNudges = 7
	cfg.applyDefaults()
	if cfg.Architect.SilenceGuard.MaxNudges != 7 {
		t.Errorf("SilenceGuard.MaxNudges = %d, want explicit 7 preserved", cfg.Architect.SilenceGuard.MaxNudges)
	}
}

// TestDefaultConfig_BlockMergeOnValidationFailDefaultsFalse is the WP8/J33 gate:
// the embedded pipeline.yaml must default this safety knob to false — today's
// behavior (Integrate always runs; validation stays advisory) must not change
// for users who don't opt in.
func TestDefaultConfig_BlockMergeOnValidationFailDefaultsFalse(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Pipeline.BlockMergeOnValidationFail {
		t.Error("Pipeline.BlockMergeOnValidationFail should default to false")
	}
}

// TestDefaultConfig_ValidatesStandalone is WP18's J29 QA gate: the embedded
// pipeline.yaml alone (DefaultConfig(), no user orqestra.yaml overlay) must
// pass validate() — "defaults" that cannot boot are not defaults. This does
// NOT change resolveConfigPath's file-required behavior in cmd/orqestra/main
// (out of scope) — it only makes the embedded defaults themselves valid, so
// a future bare-boot path has something that actually works to boot from.
//
// RED-first: against the pre-J29 embedded pipeline.yaml (no providers/models
// section at all), this failed with a *ModelNotFoundError naming "researcher"
// as the missing model_ref's context (c.Models was empty).
func TestDefaultConfig_ValidatesStandalone(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.validate(); err != nil {
		t.Fatalf("DefaultConfig().validate() = %v, want nil (embedded defaults must be bootable standalone)", err)
	}
}

// TestLoad_BlockMergeOnValidationFailOverride proves a user config can opt
// into the safety gate via pipeline.block_merge_on_validation_fail.
func TestLoad_BlockMergeOnValidationFailOverride(t *testing.T) {
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
pipeline:
  block_merge_on_validation_fail: true
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

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Pipeline.BlockMergeOnValidationFail {
		t.Error("Pipeline.BlockMergeOnValidationFail = false, want true (set via pipeline.block_merge_on_validation_fail)")
	}
}
