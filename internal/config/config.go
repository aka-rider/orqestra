package config

import (
	"fmt"
	"os"
	"regexp"
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
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// ResolvedModel is a fully-resolved model with all connection details.
type ResolvedModel struct {
	BaseURL string
	APIKey  string
	Model   string
	Type    string
}

type Config struct {
	Providers      map[string]ProviderConfig `yaml:"providers"`
	Models         map[string]ModelConfig    `yaml:"models"`
	Planner        PlannerConfig             `yaml:"planner"`
	Validator      ValidatorConfig           `yaml:"validator"`
	Worker         WorkerConfig              `yaml:"worker"`
	WorkValidator  WorkValidatorConfig       `yaml:"work_validator"`
	Retry          RetryConfig               `yaml:"retry"`
	ExecutionGraph ExecutionGraphConfig      `yaml:"execution_graph"`
	Intent         IntentConfig              `yaml:"intent"`
}

type PlannerConfig struct {
	Model        string   `yaml:"model"`
	BaseURL      string   `yaml:"base_url"`
	SystemPrompt string   `yaml:"system_prompt"`
	AllowedTools []string `yaml:"allowed_tools"`
	ModelRef     string   `yaml:"model_ref"` // optional: references key in models map
}

type ValidatorConfig struct {
	Provider     string `yaml:"provider"`
	BaseURL      string `yaml:"base_url"`
	Model        string `yaml:"model"`
	APIKey       string `yaml:"api_key"`
	SystemPrompt string `yaml:"system_prompt"`
	ModelRef     string `yaml:"model_ref"` // optional: references key in models map
}

type WorkerConfig struct {
	Model          string   `yaml:"model"`
	BaseURL        string   `yaml:"base_url"`
	AllowedTools   []string `yaml:"allowed_tools"`
	PermissionMode string   `yaml:"permission_mode"`
	Timeout        Duration `yaml:"timeout"`
	ModelRef       string   `yaml:"model_ref"` // optional: references key in models map
}

type WorkValidatorConfig struct {
	Provider     string `yaml:"provider"`
	BaseURL      string `yaml:"base_url"`
	Model        string `yaml:"model"`
	APIKey       string `yaml:"api_key"`
	SystemPrompt string `yaml:"system_prompt"`
	ModelRef     string `yaml:"model_ref"` // optional: references key in models map
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
	Role             string               `yaml:"role"`
	Model            string               `yaml:"model"`
	SmallModel       string               `yaml:"small_model"`
	SystemPromptFile string               `yaml:"system_prompt_file"`
	DependsOn        []string             `yaml:"depends_on"`
	Validator        *ValidatorNodeConfig `yaml:"validator"`
}

// ValidatorNodeConfig defines a validator attached to an agent.
type ValidatorNodeConfig struct {
	Role             string `yaml:"role"`
	Model            string `yaml:"model"`
	SystemPromptFile string `yaml:"system_prompt_file"`
}

// IntentConfig configures the intent recognition layer.
type IntentConfig struct {
	ModelRef     string `yaml:"model_ref"`
	SystemPrompt string `yaml:"system_prompt"`
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
			Model:        "qwen36",
			BaseURL:      "http://192.168.50.212:11434",
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
			Provider:     "ollama",
			BaseURL:      "http://192.168.50.212:11434",
			Model:        "qwen36",
			SystemPrompt: planValidatorSystemPrompt,
		},
		Worker: WorkerConfig{
			Model:          "qwen36",
			BaseURL:        "http://192.168.50.212:11434",
			AllowedTools:   []string{"Read", "Write", "Bash"},
			PermissionMode: "full",
			Timeout:        Duration{10 * time.Minute},
		},
		WorkValidator: WorkValidatorConfig{
			Provider:     "ollama",
			BaseURL:      "http://192.168.50.212:11434",
			Model:        "qwen36",
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
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}
	}

	// Override from environment (always applied)
	if v := os.Getenv("ORQESTRA_VALIDATOR_URL"); v != "" {
		cfg.Validator.BaseURL = v
	}
	if v := os.Getenv("ORQESTRA_VALIDATOR_MODEL"); v != "" {
		cfg.Validator.Model = v
	}
	if v := os.Getenv("ORQESTRA_VALIDATOR_API_KEY"); v != "" {
		cfg.Validator.APIKey = v
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
	}
	return nil
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
