package config

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed pipeline.yaml
var embeddedPipeline []byte

type Config struct {
	Providers  map[string]ProviderConfig `yaml:"providers"`
	Models     map[string]ModelConfig    `yaml:"models"`
	Defaults   DefaultsConfig            `yaml:"defaults"`
	Pipeline   PipelineConfig            `yaml:"pipeline"`
	Researcher ResearcherConfig          `yaml:"researcher"`
	Architect  ArchitectConfig           `yaml:"architect"`
	Critic     CriticConfig              `yaml:"critic"`
	Worker     WorkerConfig              `yaml:"worker"`
	Integrator IntegratorConfig          `yaml:"integrator"`
	Retry      RetryConfig               `yaml:"retry"`
	Sandbox    SandboxConfig             `yaml:"sandbox"`

	// ExtraRoles adds NEW pipeline roles beyond the built-ins (WP14/RC4,
	// roles.go's Config.Roles()). Named ExtraRoles, not Roles, since Config
	// already exports a Roles() method — the YAML key is still "roles". A
	// key colliding with a built-in role name is a Roles()-time error.
	ExtraRoles map[string]RoleConfig `yaml:"roles"`
}

// DefaultsConfig provides baseline values merged into every agent config.
// Agent-level fields take precedence (replacement semantics for slices).
type DefaultsConfig struct {
	DisallowedTools    []string `yaml:"disallowed_tools"`
	AppendSystemPrompt string   `yaml:"append_system_prompt"`
}

// LoopGuard configures the LoopBreaker middleware's loop-detection thresholds.
type LoopGuard struct {
	RepeatThreshold int `yaml:"repeat_threshold"` // identical tool calls before nudging (default 3)
	MaxNudges       int `yaml:"max_nudges"`       // nudges before escalating to cancel (default 3)
	CooldownTurns   int `yaml:"cooldown_turns"`   // turns to wait after a nudge before re-checking (default 2)
}

// SilenceGuard configures the SilenceDetector middleware.
type SilenceGuard struct {
	SilenceSecs int    `yaml:"silence_secs"` // seconds of event-stream silence before nudging; 0 = disabled
	NudgeText   string `yaml:"nudge_text"`   // optional; "" falls back to the agent's PreTimeoutNudge text
	MaxNudges   int    `yaml:"max_nudges"`   // nudges tolerated after a confirmed empty turn before escalating (default 3)
}

// BaseAgentConfig holds fields shared by all agent roles.
type BaseAgentConfig struct {
	Model              string       `yaml:"model"`
	Description        string       `yaml:"description"` // one-line role summary; used for inline-subagent definitions
	SystemPrompt       string       `yaml:"system_prompt"`
	AllowedTools       []string     `yaml:"allowed_tools"`
	DisallowedTools    []string     `yaml:"disallowed_tools"`
	MCPServers         *[]string    `yaml:"mcp_servers"` // nil=all, []=none, ["x"]=only x
	PermissionMode     string       `yaml:"permission_mode"`
	AppendSystemPrompt string       `yaml:"append_system_prompt"`
	Timeout            Duration     `yaml:"timeout"`
	MaxTurns           int          `yaml:"max_turns"`
	LoopGuard          LoopGuard    `yaml:"loop_guard"`
	SilenceGuard       SilenceGuard `yaml:"silence_guard"`
}

type ResearcherConfig struct {
	BaseAgentConfig `yaml:",inline"`
}

// ArchitectConfig is used for the senior architect.
type ArchitectConfig struct {
	BaseAgentConfig `yaml:",inline"`
}

// CriticConfig is used for the plan critic that reviews the architect's plan.
type CriticConfig struct {
	BaseAgentConfig `yaml:",inline"`
}

type WorkerConfig struct {
	BaseAgentConfig `yaml:",inline"`
	Parallelism     int `yaml:"parallelism"` // max concurrent workers per wave; 0 or 1 = sequential
}

// IntegratorConfig is used for the integrator agent that commits and merges
// the worker's changes into the base branch after a pipeline run.
type IntegratorConfig struct {
	BaseAgentConfig  `yaml:",inline"`
	ResolveConflicts bool `yaml:"resolve_conflicts"` // attempt LLM conflict resolution (default true)
}

type RetryConfig struct {
	ArchitectAttempts       int `yaml:"architect_attempts"`
	CriticAttempts          int `yaml:"critic_attempts"`
	WorkerValidationRetries int `yaml:"worker_validation_retries"`
}

// Duration wraps time.Duration for YAML unmarshaling.
type Duration struct {
	time.Duration
}

// PipelineConfig controls global pipeline behavior.
type PipelineConfig struct {
	TokenBudget       int64  `yaml:"token_budget"`       // total token budget for a run
	RunDir            string `yaml:"run_dir"`            // base directory for run artifacts
	WorkerConcurrency int    `yaml:"worker_concurrency"` // max concurrent workers
	// BlockMergeOnValidationFail gates Integrate on worker self-validation's
	// parsed verdict (J33/WP8): when true and the verdict is FAIL, the pipeline
	// skips Integrate and fails the run instead of merging silently. Default
	// false = today's behavior (validation stays advisory; Integrate always
	// runs) — the verdict is still threaded into Result either way.
	BlockMergeOnValidationFail bool `yaml:"block_merge_on_validation_fail"`
}

// SandboxConfig configures macOS-native sandbox (sandbox-exec) agent
// sandboxing, enforced via the leash library (internal/harness).
type SandboxConfig struct {
	MaxLifetime Duration          `yaml:"max_lifetime"` // parsed, still unread anywhere — pre-existing, out of scope
	AllowRead   []string          `yaml:"allow_read"`
	AllowWrite  []string          `yaml:"allow_write"`
	AllowExec   []string          `yaml:"allow_exec"`
	ExtraEnv    map[string]string `yaml:"extra_env"` // was silently ignored (non-strict YAML) before leash
	ProxyEnv    []string          `yaml:"proxy_env"` // same
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

func DefaultConfig() *Config {
	cfg := &Config{}
	// Load pipeline definition from compile-time embedded YAML.
	if err := yaml.Unmarshal(embeddedPipeline, cfg); err != nil {
		panic(fmt.Sprintf("embedded pipeline.yaml is invalid: %v", err))
	}
	cfg.applyDefaults()
	return cfg
}

func formatYAMLError(path string, err error) error {
	msg := err.Error()
	if strings.Contains(msg, "did not find expected key") {
		return fmt.Errorf("parsing config %q: invalid indentation or missing mandatory parameter key near %s", path, msg[strings.Index(msg, "line"):])
	}
	return fmt.Errorf("parsing config %q: %w", path, err)
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, formatYAMLError(path, err)
	}

	// Re-apply defaults after overlay so user-file agent configs
	// that didn't set disallowed_tools still inherit defaults.
	cfg.applyDefaults()

	// Validate: every model's provider key must exist
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applyDefaults merges DefaultsConfig into each agent's BaseAgentConfig,
// and applies zero-value defaults for LoopGuard thresholds. The ExtraRoles
// defaulting helper (applyRoleDefaults) lives in roles.go alongside
// RoleConfig itself (WP14/RC4).
func (c *Config) applyDefaults() {
	agents := []*BaseAgentConfig{
		&c.Researcher.BaseAgentConfig,
		&c.Architect.BaseAgentConfig,
		&c.Critic.BaseAgentConfig,
		&c.Worker.BaseAgentConfig,
		&c.Integrator.BaseAgentConfig,
	}
	for _, a := range agents {
		applyBaseAgentDefaults(a, c.Defaults)
	}
	applyRoleDefaults(c.ExtraRoles, c.Defaults)
}

// validate checks that all model references point to existing providers.
func (c *Config) validate() error {
	if c.Researcher.Model == "" {
		return fmt.Errorf("missing mandatory researcher.model parameter")
	}
	if c.Architect.Model == "" {
		return fmt.Errorf("missing mandatory architect.model parameter")
	}
	if c.Worker.Model == "" {
		return fmt.Errorf("missing mandatory worker.model parameter")
	}
	if c.Critic.Model == "" {
		return fmt.Errorf("missing mandatory critic.model parameter")
	}
	if c.Integrator.Model == "" {
		return fmt.Errorf("missing mandatory integrator.model parameter")
	}

	// Verify pipeline model refs resolve to defined model entries.
	for _, ref := range []struct{ role, ref string }{
		{"researcher", c.Researcher.Model},
		{"architect", c.Architect.Model},
		{"critic", c.Critic.Model},
		{"worker", c.Worker.Model},
		{"integrator", c.Integrator.Model},
		{"small", "small"},
	} {
		if _, key := c.lookupModel(ref.ref); key == "" {
			return &ModelNotFoundError{
				Name:       ref.ref,
				Available:  c.modelNames(),
				Context:    fmt.Sprintf("%s.model", ref.role),
				DidYouMean: c.didYouMean(ref.ref),
			}
		}
	}
	for name, m := range c.Models {
		if _, ok := c.Providers[m.Provider]; !ok {
			return fmt.Errorf("model %q references unknown provider %q", name, m.Provider)
		}
		if m.TokenLimit != "" {
			if _, err := ParseTokenLimit(m.TokenLimit); err != nil {
				return fmt.Errorf("model %q: %w", name, err)
			}
		}
	}

	// Validate provider types: every provider must declare a known type.
	// Empty or unknown types fail fast — they silently fall back to native
	// Anthropic which causes expensive token spending for intended local runs.
	for name, p := range c.Providers {
		if p.Type == "" {
			return fmt.Errorf("provider %q: type is required (valid: %q, %q, %q)", name, ProviderTypeNative, ProviderTypeAnthropic, ProviderTypeOpenAI)
		}
		if !IsProviderType(p.Type) {
			return fmt.Errorf("provider %q: unknown type %q (valid: %q, %q, %q)", name, p.Type, ProviderTypeNative, ProviderTypeAnthropic, ProviderTypeOpenAI)
		}
		if p.Type == ProviderTypeNative && p.BaseURL != "" {
			return fmt.Errorf("provider %q: type %q must not have base_url (it is ignored; use %q or %q to route to a remote endpoint)", name, ProviderTypeNative, ProviderTypeAnthropic, ProviderTypeOpenAI)
		}
		if p.Type != ProviderTypeNative && p.BaseURL == "" {
			return fmt.Errorf("provider %q: type %q requires base_url", name, p.Type)
		}
	}

	// Check for conflicting limits on the same underlying model
	if _, err := c.ResolvedTokenLimits(); err != nil {
		return err
	}

	// Roles() validates the unified role table (WP14/RC4) — unknown class,
	// unknown sandbox_tier, or empty model_ref on any entry, including
	// roles: additions the checks above never touch — fails Load closed.
	if _, err := c.Roles(); err != nil {
		return err
	}

	return nil
}
