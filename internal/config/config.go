package config

import (
	_ "embed"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed pipeline.yaml
var embeddedPipeline []byte

// Valid provider types. A provider must declare one of these types;
// an empty or unknown type is a configuration error.
const (
	ProviderTypeNative    = "native"    // Native Claude CLI (no model override env vars). Must not have base_url.
	ProviderTypeAnthropic = "anthropic" // Anthropic-compatible API (sets ANTHROPIC_BASE_URL, ANTHROPIC_MODEL, etc.)
	ProviderTypeOpenAI    = "openai"    // OpenAI-compatible API (same env vars, trailing slash trimmed from base URL)
)

// validProviderTypes is the set of recognized provider type strings.
var validProviderTypes = map[string]struct{}{
	ProviderTypeNative:    {},
	ProviderTypeAnthropic: {},
	ProviderTypeOpenAI:    {},
}

// IsProviderType reports whether the given type string is a recognized provider type.
func IsProviderType(t string) bool {
	_, ok := validProviderTypes[t]
	return ok
}

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

// ModelNotFoundError is returned when a model name cannot be resolved.
type ModelNotFoundError struct {
	Name      string
	Available []string
	Context   string
}

func (e *ModelNotFoundError) Error() string {
	msg := fmt.Sprintf("model %q not found", e.Name)
	if e.Context != "" {
		msg = fmt.Sprintf("%s: %s", msg, e.Context)
	}
	if len(e.Available) > 0 {
		msg = fmt.Sprintf("%s (available: %s)", msg, strings.Join(e.Available, ", "))
	}
	return msg
}

// Is matches ModelNotFoundError for errors.Is compatibility.
func (e *ModelNotFoundError) Is(target error) bool {
	_, ok := target.(*ModelNotFoundError)
	return ok
}

// lookupModel returns the ModelConfig and its canonical key for the given name.
// Lookup is case-insensitive. Returns (nil, "") if not found.
func (c *Config) lookupModel(name string) (*ModelConfig, string) {
	if mc, ok := c.Models[name]; ok {
		return &mc, name
	}
	for k, v := range c.Models {
		if strings.EqualFold(k, name) {
			return &v, k
		}
	}
	return nil, ""
}

// ModelMeta returns the ModelConfig and canonical key for the given model
// reference name. Uses case-insensitive lookup. Returns ok=false if the model
// is not found in the configuration.
func (c *Config) ModelMeta(name string) (ModelConfig, bool) {
	mc, _ := c.lookupModel(name)
	if mc == nil {
		return ModelConfig{}, false
	}
	return *mc, true
}

// modelNames returns a sorted list of available model names.
func (c *Config) modelNames() []string {
	names := make([]string, 0, len(c.Models))
	for k := range c.Models {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
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
	Sandbox        SandboxConfig             `yaml:"sandbox"`
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
	MaxNudges       int `yaml:"max_nudges"`        // nudges before escalating to cancel (default 3)
	CooldownTurns   int `yaml:"cooldown_turns"`    // turns to wait after a nudge before re-checking (default 2)
}

// SilenceGuard configures the SilenceDetector middleware.
type SilenceGuard struct {
	SilenceSecs int    `yaml:"silence_secs"` // seconds of event-stream silence before nudging; 0 = disabled
	NudgeText   string `yaml:"nudge_text"`   // optional; "" falls back to the agent's PreTimeoutNudge text
}

// BaseAgentConfig holds fields shared by all agent roles.
type BaseAgentConfig struct {
	Model              string       `yaml:"model"`
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

// applyDefaults merges DefaultsConfig into each agent's BaseAgentConfig,
// and applies zero-value defaults for LoopGuard thresholds.
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
		if a.LoopGuard.RepeatThreshold == 0 {
			a.LoopGuard.RepeatThreshold = 3
		}
		if a.LoopGuard.MaxNudges == 0 {
			a.LoopGuard.MaxNudges = 3
		}
		if a.LoopGuard.CooldownTurns == 0 {
			a.LoopGuard.CooldownTurns = 2
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
		if _, key := c.lookupModel(ref.ref); key == "" {
			return &ModelNotFoundError{
				Name:      ref.ref,
				Available: c.modelNames(),
				Context:   fmt.Sprintf("%s.model", ref.role),
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
// Lookup is case-insensitive.
func (c *Config) ResolveModel(name string) (ResolvedModel, error) {
	mc, _ := c.lookupModel(name)
	if mc == nil {
		return ResolvedModel{}, &ModelNotFoundError{
			Name:      name,
			Available: c.modelNames(),
		}
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
	mc, _ := c.lookupModel(name)
	if mc == nil {
		return ModelRuntimeOptions{}, &ModelNotFoundError{
			Name:      name,
			Available: c.modelNames(),
		}
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
