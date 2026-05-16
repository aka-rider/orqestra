package config

import (
	_ "embed"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed pipeline.yaml
var embeddedPipeline []byte

// ProviderConfig defines a named LLM provider endpoint.
type ProviderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"` // supports ${ENV_VAR} interpolation
	Type    string `yaml:"type"`    // "anthropic" | "openai"
}

// ModelConfig references a provider and model name.
type ModelConfig struct {
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	Binary        string `yaml:"binary"`
	TokenLimit    string `yaml:"token_limit"`    // e.g. "300K", "1M", "1.5M", "unlimited", or ""
	ContextWindow int64  `yaml:"context_window"` // context window size in tokens
}

// ResolvedModel is a fully-resolved model with all connection details.
type ResolvedModel struct {
	BaseURL string
	APIKey  string
	Model   string
	Type    string
}

// ModelRuntimeOptions captures non-connection settings for a model-backed CLI harness.
type ModelRuntimeOptions struct {
	Binary string
}

type Config struct {
	Providers      map[string]ProviderConfig `yaml:"providers"`
	Models         map[string]ModelConfig    `yaml:"models"`
	Defaults       DefaultsConfig            `yaml:"defaults"`
	Pipeline       PipelineConfig            `yaml:"pipeline"`
	Researcher     ResearcherConfig          `yaml:"researcher"`
	Architect      ArchitectConfig           `yaml:"architect"`
	Critic         CriticConfig              `yaml:"critic"`
	Worker         WorkerConfig              `yaml:"worker"`
	Retry          RetryConfig               `yaml:"retry"`
	ExecutionGraph ExecutionGraphConfig      `yaml:"execution_graph"`
	Sandbox        SandboxConfig             `yaml:"sandbox"`
}

// DefaultsConfig provides baseline values merged into every agent config.
// Agent-level fields take precedence (replacement semantics for slices).
type DefaultsConfig struct {
	DisallowedTools    []string `yaml:"disallowed_tools"`
	AppendSystemPrompt string   `yaml:"append_system_prompt"`
}

// BaseAgentConfig holds fields shared by all agent roles.
type BaseAgentConfig struct {
	Model              string    `yaml:"model"`
	SystemPrompt       string    `yaml:"system_prompt"`
	AllowedTools       []string  `yaml:"allowed_tools"`
	DisallowedTools    []string  `yaml:"disallowed_tools"`
	MCPServers         *[]string `yaml:"mcp_servers"` // nil=all, []=none, ["x"]=only x
	PermissionMode     string    `yaml:"permission_mode"`
	AppendSystemPrompt string    `yaml:"append_system_prompt"`
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
	Timeout         Duration `yaml:"timeout"`
	MaxTurns        int      `yaml:"max_turns"`
	Parallelism     int      `yaml:"parallelism"` // max concurrent workers per wave; 0 or 1 = sequential
}

type RetryConfig struct {
	ResearcherAttempts      int `yaml:"researcher_attempts"`
	ArchitectAttempts       int `yaml:"architect_attempts"`
	CriticAttempts          int `yaml:"critic_attempts"`
	WorkerValidationRetries int `yaml:"worker_validation_retries"`
}

// Duration wraps time.Duration for YAML unmarshaling.
type Duration struct {
	time.Duration
}

// ExecutionGraphConfig defines the DAG of agents for multi-agent orchestration.
type ExecutionGraphConfig struct {
	Agents      []AgentNodeConfig `yaml:"agents"`
	Concurrency int               `yaml:"concurrency"`
}

// AgentNodeConfig defines an agent within the execution graph.
type AgentNodeConfig struct {
	ID               string               `yaml:"id"`
	Role             string               `yaml:"role"`
	Kind             string               `yaml:"kind"`
	Model            string               `yaml:"model"`
	ModelRef         string               `yaml:"model_ref"`
	SmallModel       string               `yaml:"small_model"`
	SmallModelRef    string               `yaml:"small_model_ref"`
	PromptFile       string               `yaml:"prompt_file"`
	SystemPromptFile string               `yaml:"system_prompt_file"`
	DependsOn        []string             `yaml:"depends_on"`
	InputsFrom       []string             `yaml:"inputs_from"`
	Permissions      string               `yaml:"permissions"`
	Timeout          Duration             `yaml:"timeout"`
	MaxAttempts      int                  `yaml:"max_attempts"`
	OnFailure        string               `yaml:"on_failure"`
	Validator        *ValidatorNodeConfig `yaml:"validator"`
	Sandbox          *SandboxConfig       `yaml:"sandbox"` // per-agent sandbox override
}

// ValidatorNodeConfig defines a validator attached to an agent.
type ValidatorNodeConfig struct {
	ID               string `yaml:"id"`
	Role             string `yaml:"role"`
	ModelRef         string `yaml:"model_ref"`
	Model            string `yaml:"model"`
	PromptFile       string `yaml:"prompt_file"`
	SystemPromptFile string `yaml:"system_prompt_file"`
}

// PipelineConfig controls global pipeline behavior.
type PipelineConfig struct {
	TokenBudget       int64  `yaml:"token_budget"`       // total token budget for a run
	RunDir            string `yaml:"run_dir"`            // base directory for run artifacts
	WorkerConcurrency int    `yaml:"worker_concurrency"` // max concurrent workers
}

// SandboxConfig configures macOS-native sandbox (sandbox-exec) agent sandboxing.
type SandboxConfig struct {
	MaxLifetime Duration          `yaml:"max_lifetime"`
	ProxyEnv    []string          `yaml:"proxy_env"`
	ExtraEnv    map[string]string `yaml:"extra_env"`
	AllowRead   []string          `yaml:"allow_read"`
	AllowWrite  []string          `yaml:"allow_write"`
	AllowExec   []string          `yaml:"allow_exec"`
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

// applyDefaults merges DefaultsConfig into each agent's BaseAgentConfig.
// Agent-level values take precedence: if an agent already has DisallowedTools
// or AppendSystemPrompt set, the default is not applied (replacement semantics).
func (c *Config) applyDefaults() {
	agents := []*BaseAgentConfig{
		&c.Researcher.BaseAgentConfig,
		&c.Architect.BaseAgentConfig,
		&c.Critic.BaseAgentConfig,
		&c.Worker.BaseAgentConfig,
	}
	for _, a := range agents {
		if len(a.DisallowedTools) == 0 && len(c.Defaults.DisallowedTools) > 0 {
			a.DisallowedTools = append([]string(nil), c.Defaults.DisallowedTools...)
		}
		if a.AppendSystemPrompt == "" && c.Defaults.AppendSystemPrompt != "" {
			a.AppendSystemPrompt = c.Defaults.AppendSystemPrompt
		}
	}
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

	// Verify pipeline model refs resolve to defined model entries.
	for _, ref := range []struct{ role, ref string }{
		{"researcher", c.Researcher.Model},
		{"architect", c.Architect.Model},
		{"critic", c.Critic.Model},
		{"worker", c.Worker.Model},
		{"small", "small"},
	} {
		if _, ok := c.Models[ref.ref]; !ok {
			// Case-insensitive lookup
			found := false
			for k := range c.Models {
				if strings.EqualFold(k, ref.ref) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%s.model %q not found in models (define model %q in your provider config)", ref.role, ref.ref, ref.ref)
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
	// Check for conflicting limits on the same underlying model
	if _, err := c.ResolvedTokenLimits(); err != nil {
		return err
	}
	for _, node := range c.ExecutionGraph.Agents {
		if node.ID == "" && node.Role == "" {
			return fmt.Errorf("execution graph agent missing mandatory id or role parameter")
		}
		if node.ModelRef != "" {
			if _, ok := c.Models[node.ModelRef]; !ok {
				return fmt.Errorf("execution graph node %q references unknown model_ref %q", node.identity(), node.ModelRef)
			}
		}
		if node.SmallModelRef != "" {
			if _, ok := c.Models[node.SmallModelRef]; !ok {
				return fmt.Errorf("execution graph node %q references unknown small_model_ref %q", node.identity(), node.SmallModelRef)
			}
		}
		if node.Validator != nil && node.Validator.ModelRef != "" {
			if _, ok := c.Models[node.Validator.ModelRef]; !ok {
				return fmt.Errorf("execution graph validator %q references unknown model_ref %q", node.Validator.identity(), node.Validator.ModelRef)
			}
		}
	}

	return nil
}

func (n AgentNodeConfig) identity() string {
	if n.ID != "" {
		return n.ID
	}
	if n.Role != "" {
		return n.Role
	}
	return "<unnamed>"
}

func (n ValidatorNodeConfig) identity() string {
	if n.ID != "" {
		return n.ID
	}
	if n.Role != "" {
		return n.Role
	}
	return "<unnamed>"
}

// envVarPattern matches ${VAR_NAME} for environment variable interpolation.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// interpolateEnv expands ${VAR} patterns using os.Getenv.
func interpolateEnv(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := envVarPattern.FindStringSubmatch(match)[1]
		return os.Getenv(varName)
	})
}

// ResolveModel resolves a model name from the models map into a ResolvedModel.
// Lookup is case-insensitive.
func (c *Config) ResolveModel(name string) (ResolvedModel, error) {
	mc, ok := c.Models[name]
	if !ok {
		// Case-insensitive fallback
		for k, v := range c.Models {
			if strings.EqualFold(k, name) {
				mc = v
				ok = true
				break
			}
		}
	}
	if !ok {
		return ResolvedModel{}, fmt.Errorf("model %q not found in config", name)
	}

	pc, ok := c.Providers[mc.Provider]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("provider %q not found for model %q", mc.Provider, name)
	}

	return ResolvedModel{
		BaseURL: pc.BaseURL,
		APIKey:  interpolateEnv(pc.APIKey),
		Model:   mc.Model,
		Type:    pc.Type,
	}, nil
}

// RuntimeOptions returns CLI harness options stored next to a named model.
func (c *Config) RuntimeOptions(name string) (ModelRuntimeOptions, error) {
	mc, ok := c.Models[name]
	if !ok {
		return ModelRuntimeOptions{}, fmt.Errorf("model %q not found in config", name)
	}

	return ModelRuntimeOptions{
		Binary: mc.Binary,
	}, nil
}

// ResolveUtilityModel resolves the utility model. Returns nil if not defined.
func (c *Config) ResolveUtilityModel() *ResolvedModel {
	resolved, err := c.ResolveModel("small")
	if err != nil {
		return nil
	}
	return &resolved
}

// TokenLimitUnlimited is the sentinel value representing no cap.
const TokenLimitUnlimited int64 = -1

// ParseTokenLimit parses a human-friendly token limit string.
// Accepted formats: "300K", "1M", "1.5M", "500000", "unlimited", "".
// Returns 0 for empty (unconfigured), -1 for unlimited, positive value otherwise.
func ParseTokenLimit(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.EqualFold(s, "unlimited") {
		return TokenLimitUnlimited, nil
	}

	lower := strings.ToLower(s)
	var multiplier float64
	var numStr string

	switch {
	case strings.HasSuffix(lower, "k"):
		multiplier = 1_000
		numStr = s[:len(s)-1]
	case strings.HasSuffix(lower, "m"):
		multiplier = 1_000_000
		numStr = s[:len(s)-1]
	default:
		multiplier = 1
		numStr = s
	}

	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid token_limit %q: %w", s, err)
	}
	if val <= 0 {
		return 0, fmt.Errorf("invalid token_limit %q: must be positive", s)
	}

	result := int64(math.Round(val * multiplier))
	if result <= 0 {
		return 0, fmt.Errorf("invalid token_limit %q: resolved to non-positive value", s)
	}
	return result, nil
}

// ResolvedTokenLimits returns a map of underlying model string → parsed token limit
// for all models that have a non-empty token_limit configured.
// Returns only models with active limits (excludes unlimited and unconfigured).
func (c *Config) ResolvedTokenLimits() (map[string]int64, error) {
	limits := make(map[string]int64)
	for name, mc := range c.Models {
		if mc.TokenLimit == "" {
			continue
		}
		parsed, err := ParseTokenLimit(mc.TokenLimit)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", name, err)
		}
		if parsed == TokenLimitUnlimited || parsed == 0 {
			continue
		}
		existing, exists := limits[mc.Model]
		if exists && existing != parsed {
			return nil, fmt.Errorf("conflicting token_limit for model %q: %d vs %d (from config entry %q)",
				mc.Model, existing, parsed, name)
		}
		limits[mc.Model] = parsed
	}
	return limits, nil
}
