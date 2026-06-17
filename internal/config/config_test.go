package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Researcher.Model != "medium" {
		t.Errorf("researcher model = %q, want %q", cfg.Researcher.Model, "medium")
	}
	if cfg.Architect.Model != "large" {
		t.Errorf("architect model = %q, want %q", cfg.Architect.Model, "large")
	}
	if cfg.Critic.Model != "medium" {
		t.Errorf("critic model = %q, want %q", cfg.Critic.Model, "medium")
	}
	if cfg.Worker.Model != "medium" {
		t.Errorf("worker model = %q, want %q", cfg.Worker.Model, "medium")
	}
	if cfg.Retry.ResearcherAttempts < 1 {
		t.Error("researcher attempts should be at least 1")
	}
	if cfg.Retry.ArchitectAttempts < 1 {
		t.Error("architect attempts should be at least 1")
	}
	if cfg.Retry.CriticAttempts < 1 {
		t.Error("critic attempts should be at least 1")
	}
	if cfg.Critic.SystemPrompt == "" {
		t.Error("critic system prompt should be set from embedded pipeline.yaml")
	}
	if cfg.Critic.PermissionMode != "plan" {
		t.Errorf("critic permission_mode = %q, want %q", cfg.Critic.PermissionMode, "plan")
	}
}

func TestDefaultConfig_DefaultsMerged(t *testing.T) {
	cfg := DefaultConfig()

	// defaults.disallowed_tools should be set in pipeline.yaml
	if len(cfg.Defaults.DisallowedTools) == 0 {
		t.Fatal("defaults.disallowed_tools should be set from embedded pipeline.yaml")
	}
	found := false
	for _, tool := range cfg.Defaults.DisallowedTools {
		if tool == "AskUserQuestion" {
			found = true
		}
	}
	if !found {
		t.Errorf("defaults.disallowed_tools = %v, want AskUserQuestion included", cfg.Defaults.DisallowedTools)
	}

	// defaults.append_system_prompt should be set
	if cfg.Defaults.AppendSystemPrompt == "" {
		t.Error("defaults.append_system_prompt should be set from embedded pipeline.yaml")
	}

	// Plan-mode agents override disallowed_tools (with ExitPlanMode added),
	// so defaults should NOT overwrite their explicit lists.
	for _, tool := range cfg.Researcher.DisallowedTools {
		if tool == "ExitPlanMode" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("researcher.disallowed_tools = %v, want ExitPlanMode included", cfg.Researcher.DisallowedTools)
	}

	// Worker has no explicit disallowed_tools, so should inherit defaults.
	found = false
	for _, tool := range cfg.Worker.DisallowedTools {
		if tool == "AskUserQuestion" {
			found = true
		}
	}
	if !found {
		t.Errorf("worker.disallowed_tools = %v, want AskUserQuestion inherited from defaults", cfg.Worker.DisallowedTools)
	}
}

func TestApplyDefaults_AgentOverridePrevails(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{
			DisallowedTools:    []string{"AskUserQuestion"},
			AppendSystemPrompt: "default nudge",
		},
		Researcher: ResearcherConfig{BaseAgentConfig: BaseAgentConfig{
			Model:           "medium",
			DisallowedTools: []string{"AskUserQuestion", "ExitPlanMode"},
		}},
		Architect: ArchitectConfig{BaseAgentConfig: BaseAgentConfig{
			Model:              "large",
			AppendSystemPrompt: "custom architect nudge",
		}},
		Critic: CriticConfig{BaseAgentConfig: BaseAgentConfig{
			Model: "medium",
		}},
		Worker: WorkerConfig{BaseAgentConfig: BaseAgentConfig{
			Model: "medium",
		}},
	}
	cfg.applyDefaults()

	// Researcher has explicit disallowed_tools — should NOT be overwritten
	if len(cfg.Researcher.DisallowedTools) != 2 {
		t.Errorf("researcher.disallowed_tools = %v, want [AskUserQuestion, ExitPlanMode]", cfg.Researcher.DisallowedTools)
	}
	// Researcher has no append_system_prompt — should inherit default
	if cfg.Researcher.AppendSystemPrompt != "default nudge" {
		t.Errorf("researcher.append_system_prompt = %q, want %q", cfg.Researcher.AppendSystemPrompt, "default nudge")
	}
	// Architect has explicit append_system_prompt — should NOT be overwritten
	if cfg.Architect.AppendSystemPrompt != "custom architect nudge" {
		t.Errorf("architect.append_system_prompt = %q, want %q", cfg.Architect.AppendSystemPrompt, "custom architect nudge")
	}
	// Architect has no disallowed_tools — should inherit default
	if len(cfg.Architect.DisallowedTools) != 1 || cfg.Architect.DisallowedTools[0] != "AskUserQuestion" {
		t.Errorf("architect.disallowed_tools = %v, want [AskUserQuestion]", cfg.Architect.DisallowedTools)
	}
	// Critic has nothing — should inherit both
	if cfg.Critic.AppendSystemPrompt != "default nudge" {
		t.Errorf("critic.append_system_prompt = %q, want %q", cfg.Critic.AppendSystemPrompt, "default nudge")
	}
	if len(cfg.Critic.DisallowedTools) != 1 || cfg.Critic.DisallowedTools[0] != "AskUserQuestion" {
		t.Errorf("critic.disallowed_tools = %v, want [AskUserQuestion]", cfg.Critic.DisallowedTools)
	}
}

// Contract: config.go validate() — every agent role must have a non-empty model reference
func TestValidate_MissingRoleModels(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"missing researcher model", func(c *Config) { c.Researcher.Model = "" }, "researcher"},
		{"missing architect model", func(c *Config) { c.Architect.Model = "" }, "architect"},
		{"missing worker model", func(c *Config) { c.Worker.Model = "" }, "worker"},
		{"missing critic model", func(c *Config) { c.Critic.Model = "" }, "critic"},
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
	_, err := Load("nonexistent.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoad_ValidYAML(t *testing.T) {
	content := `
providers:
  local:
    base_url: http://test:1234
    type: openai
models:
  medium:
    provider: local
    model: test-planner
  small:
    provider: local
    model: test-val
  large:
    provider: local
    model: test-large
researcher:
  model: medium
architect:
  model: large
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
	if cfg.Researcher.Model != "medium" {
		t.Errorf("expected researcher model medium, got %q", cfg.Researcher.Model)
	}
	if cfg.Architect.Model != "large" {
		t.Errorf("expected architect model large, got %q", cfg.Architect.Model)
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

func TestResolveModel_CaseInsensitive(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"prov": {BaseURL: "http://localhost", Type: "openai"},
		},
		Models: map[string]ModelConfig{
			"Medium": {Provider: "prov", Model: "claude-medium"},
		},
	}
	resolved, err := cfg.ResolveModel("medium")
	if err != nil {
		t.Fatalf("expected case-insensitive lookup to succeed, got: %v", err)
	}
	if resolved.Model != "claude-medium" {
		t.Errorf("Model = %q, want claude-medium", resolved.Model)
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
	var notFound *ModelNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *ModelNotFoundError, got %T", err)
	}
	if notFound.Name != "nonexistent" {
		t.Errorf("Name = %q, want %q", notFound.Name, "nonexistent")
	}
	if notFound.Context != "" {
		t.Errorf("Context = %q, want empty", notFound.Context)
	}
}

func TestResolveModel_MissingModel_SuggestsAvailable(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {BaseURL: "http://localhost", Type: "openai"},
		},
		Models: map[string]ModelConfig{
			"Medium": {Provider: "local", Model: "claude-medium"},
			"Small":  {Provider: "local", Model: "claude-small"},
		},
	}

	_, err := cfg.ResolveModel("tiny")
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if !strings.Contains(err.Error(), "Medium") || !strings.Contains(err.Error(), "Small") {
		t.Errorf("error should list available models, got: %s", err)
	}
}

func TestModelNotFoundError_Is(t *testing.T) {
	err := &ModelNotFoundError{Name: "foo"}
	var target error = &ModelNotFoundError{}
	if !errors.Is(err, target) {
		t.Error("errors.Is should match ModelNotFoundError")
	}
}

func TestModelNotFoundError_Error_Fields(t *testing.T) {
	tests := []struct {
		name string
		err  *ModelNotFoundError
		want []string
	}{
		{
			name: "basic",
			err:  &ModelNotFoundError{Name: "foo"},
			want: []string{`model "foo" not found`},
		},
		{
			name: "with context",
			err:  &ModelNotFoundError{Name: "foo", Context: "researcher.model"},
			want: []string{`model "foo" not found: researcher.model`},
		},
		{
			name: "with available",
			err:  &ModelNotFoundError{Name: "foo", Available: []string{"a", "b"}},
			want: []string{`(available: a, b)`},
		},
		{
			name: "all fields",
			err:  &ModelNotFoundError{Name: "foo", Context: "researcher.model", Available: []string{"m", "s"}},
			want: []string{`model "foo" not found: researcher.model (available: m, s)`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("error = %q, should contain %q", got, w)
				}
			}
		})
	}
}

func TestLookupModel_CaseInsensitive(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"Medium": {Provider: "prov", Model: "x"},
		},
	}
	mc, key := cfg.lookupModel("medium")
	if mc == nil {
		t.Fatal("expected case-insensitive lookup to succeed")
	}
	if key != "Medium" {
		t.Errorf("key = %q, want Medium", key)
	}
}

func TestLookupModel_NotFound(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"foo": {Provider: "prov", Model: "x"},
		},
	}
	mc, key := cfg.lookupModel("bar")
	if mc != nil {
		t.Errorf("mc = %+v, want nil", mc)
	}
	if key != "" {
		t.Errorf("key = %q, want empty", key)
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

func TestRuntimeOptions(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {BaseURL: "http://localhost", Type: "openai"},
		},
		Models: map[string]ModelConfig{
			"worker": {
				Provider: "local",
				Model:    "qwen36",
				Binary:   "claude-test",
			},
		},
	}

	runtime, err := cfg.RuntimeOptions("worker")
	if err != nil {
		t.Fatalf("RuntimeOptions() error: %v", err)
	}
	if runtime.Binary != "claude-test" {
		t.Errorf("Binary = %q, want claude-test", runtime.Binary)
	}
}

func TestResolveUtilityModel(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {BaseURL: "http://localhost:1234", APIKey: "k", Type: "openai"},
		},
		Models: map[string]ModelConfig{
			"small": {Provider: "local", Model: "haiku"},
		},
	}

	utility := cfg.ResolveUtilityModel()
	if utility == nil {
		t.Fatal("expected utility model to resolve")
	}
	if utility.Model != "haiku" {
		t.Errorf("utility.Model = %q, want haiku", utility.Model)
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
			"architect": {Provider: "local", Model: "opus", TokenLimit: "300K"},
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

func TestLoad_SandboxConfig(t *testing.T) {
	yaml := `
providers:
  test:
    base_url: http://localhost
    api_key: dummy
    type: openai
models:
  medium:
    provider: test
    model: test-medium
    token_limit: 100K
  small:
    provider: test
    model: test-small
    token_limit: 50K
researcher:
  model: medium
architect:
  model: medium
worker:
  model: medium
sandbox:
  max_lifetime: 2h
  proxy_env:
    - AWS_PROFILE
    - SSH_AUTH_SOCK
  extra_env:
    NODE_ENV: development
    CUSTOM_VAR: hello
  allow_read:
    - ~/.dotfiles
    - ~/.aws/config
  allow_write:
    - /tmp/my-cache
  allow_exec:
    - /opt/homebrew/bin
`
	f := filepath.Join(t.TempDir(), "cfg.yaml")
	os.WriteFile(f, []byte(yaml), 0644)

	cfg, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Sandbox.MaxLifetime.Duration != 2*time.Hour {
		t.Errorf("max_lifetime = %v, want 2h", cfg.Sandbox.MaxLifetime.Duration)
	}
	if len(cfg.Sandbox.ProxyEnv) != 2 || cfg.Sandbox.ProxyEnv[0] != "AWS_PROFILE" {
		t.Errorf("proxy_env = %v, want [AWS_PROFILE SSH_AUTH_SOCK]", cfg.Sandbox.ProxyEnv)
	}
	if cfg.Sandbox.ExtraEnv["NODE_ENV"] != "development" {
		t.Errorf("extra_env[NODE_ENV] = %q, want development", cfg.Sandbox.ExtraEnv["NODE_ENV"])
	}
	if len(cfg.Sandbox.AllowRead) != 2 {
		t.Errorf("allow_read length = %d, want 2", len(cfg.Sandbox.AllowRead))
	}
	if len(cfg.Sandbox.AllowWrite) != 1 || cfg.Sandbox.AllowWrite[0] != "/tmp/my-cache" {
		t.Errorf("allow_write = %v, want [/tmp/my-cache]", cfg.Sandbox.AllowWrite)
	}
	if len(cfg.Sandbox.AllowExec) != 1 || cfg.Sandbox.AllowExec[0] != "/opt/homebrew/bin" {
		t.Errorf("allow_exec = %v, want [/opt/homebrew/bin]", cfg.Sandbox.AllowExec)
	}
}

func TestLoad_SandboxDefaults(t *testing.T) {
	yaml := `
providers:
  test:
    base_url: http://localhost
    api_key: dummy
    type: openai
models:
  medium:
    provider: test
    model: test-medium
    token_limit: 100K
  small:
    provider: test
    model: test-small
    token_limit: 50K
researcher:
  model: medium
architect:
  model: medium
worker:
  model: medium
`
	f := filepath.Join(t.TempDir(), "cfg.yaml")
	os.WriteFile(f, []byte(yaml), 0644)

	cfg, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Sandbox.MaxLifetime.Duration != 45*time.Minute {
		t.Errorf("max_lifetime default = %v, want 45m (from embedded pipeline.yaml)", cfg.Sandbox.MaxLifetime.Duration)
	}
	if len(cfg.Sandbox.ProxyEnv) != 0 {
		t.Errorf("proxy_env default = %v, want empty", cfg.Sandbox.ProxyEnv)
	}
	if len(cfg.Sandbox.ExtraEnv) != 0 {
		t.Errorf("extra_env default = %v, want empty", cfg.Sandbox.ExtraEnv)
	}
	if len(cfg.Sandbox.AllowRead) != 0 {
		t.Errorf("allow_read default = %v, want empty", cfg.Sandbox.AllowRead)
	}
}

func TestLoad_SandboxGlobalConfig(t *testing.T) {
	yaml := `
providers:
  test:
    base_url: http://localhost
    api_key: dummy
    type: openai
models:
  medium:
    provider: test
    model: test-medium
    token_limit: 100K
  small:
    provider: test
    model: test-small
    token_limit: 50K
researcher:
  model: medium
architect:
  model: medium
worker:
  model: medium
sandbox:
  max_lifetime: 1h
  allow_exec:
    - /opt/homebrew/bin
`
	f := filepath.Join(t.TempDir(), "cfg.yaml")
	os.WriteFile(f, []byte(yaml), 0644)

	cfg, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Sandbox.MaxLifetime.Duration != 1*time.Hour {
		t.Errorf("global max_lifetime = %v, want 1h", cfg.Sandbox.MaxLifetime.Duration)
	}
	if len(cfg.Sandbox.AllowExec) != 1 || cfg.Sandbox.AllowExec[0] != "/opt/homebrew/bin" {
		t.Errorf("allow_exec = %v, want [/opt/homebrew/bin]", cfg.Sandbox.AllowExec)
	}
}

func TestModelMeta_Found(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"large": {Provider: "anthropic", Model: "claude-opus-4", ContextWindow: 200000},
		},
	}
	mc, ok := cfg.ModelMeta("large")
	if !ok {
		t.Fatal("ModelMeta(large) returned ok=false")
	}
	if mc.Model != "claude-opus-4" {
		t.Errorf("Model = %q, want claude-opus-4", mc.Model)
	}
	if mc.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", mc.ContextWindow)
	}
}

func TestModelMeta_CaseInsensitive(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"Large": {Provider: "anthropic", Model: "claude-opus-4", ContextWindow: 200000},
		},
	}
	mc, ok := cfg.ModelMeta("large")
	if !ok {
		t.Fatal("ModelMeta(large) should find 'Large' case-insensitively")
	}
	if mc.Model != "claude-opus-4" {
		t.Errorf("Model = %q, want claude-opus-4", mc.Model)
	}
}

func TestModelMeta_NotFound(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"large": {Provider: "anthropic", Model: "claude-opus-4"},
		},
	}
	_, ok := cfg.ModelMeta("nonexistent")
	if ok {
		t.Error("ModelMeta(nonexistent) should return ok=false")
	}
}

func TestValidate_ProviderType(t *testing.T) {
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

func TestIsProviderType(t *testing.T) {
	tests := []struct {
		typ    string
		expect bool
	}{
		{ProviderTypeNative, true},
		{ProviderTypeAnthropic, true},
		{ProviderTypeOpenAI, true},
		{"", false},
		{"copilot-proxy", false},
		{"llama-cpp", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			if got := IsProviderType(tt.typ); got != tt.expect {
				t.Errorf("IsProviderType(%q) = %v, want %v", tt.typ, got, tt.expect)
			}
		})
	}
}
