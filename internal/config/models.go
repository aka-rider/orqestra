package config

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

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
	// DidYouMean lists model names that match Name case-insensitively (J31c)
	// — e.g. "Medium" configured against a "medium" alias — so an exact-match
	// miss caused by a typo's case is diagnosable instead of silently masked.
	DidYouMean []string
}

func (e *ModelNotFoundError) Error() string {
	msg := fmt.Sprintf("model %q not found", e.Name)
	if e.Context != "" {
		msg = fmt.Sprintf("%s: %s", msg, e.Context)
	}
	if len(e.DidYouMean) > 0 {
		msg = fmt.Sprintf("%s (did you mean: %s?)", msg, strings.Join(e.DidYouMean, ", "))
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

// lookupModel returns the ModelConfig and its canonical key for the given
// name. Lookup is EXACT (J31c): a case-insensitive fallback used to let a
// typo'd model_ref like "Medium" silently resolve to the "medium" alias,
// masking the misconfiguration instead of surfacing it. Returns (nil, "") if
// not found; callers that need a helpful message on a miss use didYouMean
// below.
func (c *Config) lookupModel(name string) (*ModelConfig, string) {
	if mc, ok := c.Models[name]; ok {
		return &mc, name
	}
	return nil, ""
}

// didYouMean returns the sorted (§1.7) set of model names in c.Models that
// match name case-insensitively but not exactly — candidates for a
// ModelNotFoundError hint (J31c) when an exact lookupModel misses.
func (c *Config) didYouMean(name string) []string {
	var candidates []string
	for k := range c.Models {
		if k != name && strings.EqualFold(k, name) {
			candidates = append(candidates, k)
		}
	}
	sort.Strings(candidates)
	return candidates
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
// Lookup is exact (J31c).
func (c *Config) ResolveModel(name string) (ResolvedModel, error) {
	mc, _ := c.lookupModel(name)
	if mc == nil {
		return ResolvedModel{}, &ModelNotFoundError{
			Name:       name,
			Available:  c.modelNames(),
			DidYouMean: c.didYouMean(name),
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
			Name:       name,
			Available:  c.modelNames(),
			DidYouMean: c.didYouMean(name),
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
