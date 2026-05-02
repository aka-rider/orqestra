package config

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProviderConfig defines a named LLM provider endpoint.
type ProviderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"` // supports ${ENV_VAR} interpolation
	Type    string `yaml:"type"`    // "anthropic" | "openai"
}

// ModelConfig references a provider and model name.
type ModelConfig struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
	SmallRef   string `yaml:"small_ref"`
	Binary     string `yaml:"binary"`
	TokenLimit string `yaml:"token_limit"` // e.g. "300K", "1M", "1.5M", "unlimited", or ""
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
	SmallRef string
	Binary   string
}

type Config struct {
	Providers      map[string]ProviderConfig `yaml:"providers"`
	Models         map[string]ModelConfig    `yaml:"models"`
	Planner        PlannerConfig             `yaml:"planner"`
	Validator      ValidatorConfig           `yaml:"validator"`
	Worker         WorkerConfig              `yaml:"worker"`
	WorkValidator  ValidatorConfig           `yaml:"work_validator"`
	Retry          RetryConfig               `yaml:"retry"`
	ExecutionGraph ExecutionGraphConfig      `yaml:"execution_graph"`
	Intent         IntentConfig              `yaml:"intent"`
	Sandbox        SandboxConfig             `yaml:"sandbox"`
	// LlamaEndpoint is the base URL of a running llama-server OpenAI-compatible
	// endpoint used by the HTTP work validator. Required for work validation.
	LlamaEndpoint string `yaml:"llama_endpoint"`
}

type PlannerConfig struct {
	ModelRef     string   `yaml:"model_ref"`
	SystemPrompt string   `yaml:"system_prompt"`
	AllowedTools []string `yaml:"allowed_tools"`
}

// ValidatorConfig is used for both plan and work validation.
type ValidatorConfig struct {
	ModelRef     string `yaml:"model_ref"`
	SystemPrompt string `yaml:"system_prompt"`
}

type WorkerConfig struct {
	ModelRef       string   `yaml:"model_ref"`
	AllowedTools   []string `yaml:"allowed_tools"`
	PermissionMode string   `yaml:"permission_mode"`
	Timeout        Duration `yaml:"timeout"`
}

type RetryConfig struct {
	PlannerAttempts      int `yaml:"planner_attempts"`
	PlanValidationRepair int `yaml:"plan_validation_repair"`
	WorkValidationRepair int `yaml:"work_validation_repair"`
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

// IntentConfig configures the intent recognition layer.
type IntentConfig struct {
	ModelRef     string `yaml:"model_ref"`
	SystemPrompt string `yaml:"system_prompt"`
}

// SandboxConfig configures Docker-based agent sandboxing.
type SandboxConfig struct {
	Enabled            bool             `yaml:"enabled"`
	Image              string           `yaml:"image"`
	Memory             string           `yaml:"memory"`       // e.g. "4g"
	CPUs               float64          `yaml:"cpus"`         // e.g. 2.0
	PidsLimit          int64            `yaml:"pids_limit"`   // max PIDs in container
	MaxLifetime        Duration         `yaml:"max_lifetime"` // hard kill after this
	ReadOnlyMounts     []SandboxMount   `yaml:"read_only_mounts"`
	AllowedExecutables []string         `yaml:"allowed_executables"` // glob patterns
	MCP                SandboxMCPConfig `yaml:"mcp"`
}

// SandboxMount describes a host path to mount read-only inside the sandbox.
type SandboxMount struct {
	Host      string `yaml:"host"`
	Container string `yaml:"container"`
}

// SandboxMCPConfig configures MCP server access from within sandboxes.
type SandboxMCPConfig struct {
	SocketPath string `yaml:"socket_path"` // Docker MCP gateway socket path on host
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
	return &Config{
		Planner: PlannerConfig{
			ModelRef:     "qwen3.6",
			AllowedTools: []string{"Read", "Write"},
			SystemPrompt: `You are Claude operating in /plan mode.

Your sole job is to decompose a user task into an executable specification.
Think step-by-step. Be specific and concrete. No hand-waving.

Output a JSON object with exactly these fields:
- "goal": a one-sentence summary of what needs to be done
- "steps": an ordered array of concrete implementation steps (each step is actionable by a worker agent)
- "acceptance": an array of verifiable criteria that define when the task is complete

Respond ONLY with valid JSON. No markdown fences, no commentary.`,
		},
		Validator: ValidatorConfig{
			ModelRef:     "qwen3.6",
			SystemPrompt: planValidatorSystemPrompt,
		},
		Worker: WorkerConfig{
			ModelRef:       "qwen3.6",
			AllowedTools:   []string{"Read", "Write", "Bash"},
			PermissionMode: "full",
			Timeout:        Duration{10 * time.Minute},
		},
		WorkValidator: ValidatorConfig{
			ModelRef:     "qwen3.6",
			SystemPrompt: workValidatorSystemPrompt,
		},
		Retry: RetryConfig{
			PlannerAttempts:      2,
			PlanValidationRepair: 3,
			WorkValidationRepair: 1,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, err)
	}

	// Override from environment (always applied)
	if v := os.Getenv("ORQESTRA_VALIDATOR_MODEL_REF"); v != "" {
		cfg.Validator.ModelRef = v
	}
	if v := os.Getenv("ORQESTRA_WORK_VALIDATOR_MODEL_REF"); v != "" {
		cfg.WorkValidator.ModelRef = v
	}

	// Validate: every model's provider key must exist
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate checks that all model references point to existing providers.
func (c *Config) validate() error {
	for name, m := range c.Models {
		if _, ok := c.Providers[m.Provider]; !ok {
			return fmt.Errorf("model %q references unknown provider %q", name, m.Provider)
		}
		if m.SmallRef != "" {
			if _, ok := c.Models[m.SmallRef]; !ok {
				return fmt.Errorf("model %q references unknown small_ref %q", name, m.SmallRef)
			}
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
func (c *Config) ResolveModel(name string) (ResolvedModel, error) {
	mc, ok := c.Models[name]
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
		SmallRef: mc.SmallRef,
		Binary:   mc.Binary,
	}, nil
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

const planValidatorSystemPrompt = `You are an independent plan validator. Your job is to judge whether a specification is complete, executable, non-contradictory, and testable.

Analyze the specification and produce a JSON validation report with exactly these fields:
- "schema_version": "1"
- "verdict": one of "pass", "warn", or "fail"
- "summary": one-sentence overall assessment
- "issues": array of objects with "id", "severity" ("error"|"warning"|"info"), "message", and optional "location"
- "suggestions": array of improvement suggestions (strings)

Rules:
- "fail" if any step is ambiguous, contradictory, or impossible
- "fail" if acceptance criteria are unmeasurable
- "warn" if steps could be more specific but are workable
- "pass" if the plan is clear, ordered, and testable

Respond ONLY with valid JSON. No markdown fences, no commentary.`

const workValidatorSystemPrompt = `You are an independent work validator. Your job is to verify that execution output satisfies the original specification.

You will receive the original specification and the execution output/logs. Produce a JSON validation report with exactly these fields:
- "schema_version": "1"
- "verdict": one of "pass", "warn", or "fail"
- "summary": one-sentence overall assessment
- "issues": array of objects with "id", "severity" ("error"|"warning"|"info"), "message", and optional "location"
- "suggestions": array of improvement suggestions (strings)

Rules:
- "fail" if any acceptance criterion is clearly unmet
- "warn" if work appears complete but evidence is ambiguous
- "pass" if all acceptance criteria are demonstrably satisfied

Respond ONLY with valid JSON. No markdown fences, no commentary.`
