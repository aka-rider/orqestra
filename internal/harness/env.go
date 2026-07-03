package harness

import (
	"fmt"
	"os"
	"strings"

	"github.com/xiii/orqestra/internal/config"
)

// localAuthSentinel is the non-empty placeholder ANTHROPIC_API_KEY emitted for a
// non-native provider that declares no api_key (e.g. a local Ollama / llama.cpp
// endpoint that needs no auth). A non-empty key forces the claude CLI into
// key-auth against the configured ANTHROPIC_BASE_URL instead of silently falling
// back to the on-disk subscription OAuth — which would bill api.anthropic.com
// while the user believes the run is local. The local endpoint ignores the value;
// an endpoint that does require a real key will fail closed (401) rather than fall
// back. See plan: silent-subscription-billing fix.
const localAuthSentinel = "orqestra-local"

// modelAuthToken returns the ANTHROPIC_API_KEY to emit for a non-native provider:
// the configured key when present, else the local sentinel.
func modelAuthToken(apiKey string) string {
	if apiKey != "" {
		return apiKey
	}
	return localAuthSentinel
}

// BuildModelEnv returns the environment variables needed to route the claude binary
// to the given model. Used by sandbox runners that exec claude inside a container.
// Returns an error if the provider type is empty or unknown — no fallback to native.
//
// For non-native providers it always emits a non-empty ANTHROPIC_API_KEY (the
// configured key or localAuthSentinel) so the configured base URL is actually used
// and an unreachable/keyless endpoint fails closed instead of silently billing the
// user's subscription.
func BuildModelEnv(resolved config.ResolvedModel, utility *config.ResolvedModel) ([]string, error) {
	switch resolved.Type {
	case config.ProviderTypeNative:
		// no override — use native Claude CLI with logged-in credentials
		return nil, nil
	case config.ProviderTypeAnthropic:
		env := []string{
			"ANTHROPIC_BASE_URL=" + resolved.BaseURL,
			"ANTHROPIC_MODEL=" + resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL=" + resolved.Model,
			"ANTHROPIC_API_KEY=" + modelAuthToken(resolved.APIKey),
		}
		if utility != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+utility.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+utility.Model,
			)
		}
		return env, nil
	case config.ProviderTypeOpenAI:
		baseURL := strings.TrimRight(resolved.BaseURL, "/")
		env := []string{
			"ANTHROPIC_BASE_URL=" + baseURL,
			"ANTHROPIC_MODEL=" + resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL=" + resolved.Model,
			"ANTHROPIC_API_KEY=" + modelAuthToken(resolved.APIKey),
		}
		if utility != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+utility.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+utility.Model,
			)
		}
		return env, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q for model %q (valid: %q, %q, %q)",
			resolved.Type, resolved.Model,
			config.ProviderTypeNative, config.ProviderTypeAnthropic, config.ProviderTypeOpenAI)
	}
}

// buildProcessEnv constructs the subprocess environment: model-routing env
// vars overlaid onto a filtered copy of the parent environment. Filters
// variables that would make the subprocess behave as a child session of the
// invoking Claude Code process — CLAUDE_CODE_SESSION_ID and
// CLAUDE_CODE_CHILD_SESSION cause claude 2.1+ to attempt parent-session IPC
// instead of starting an independent session, resulting in a hang. Also
// strips ANTHROPIC_API_KEY to prevent leakage or conflicts.
//
// This is THE environment builder (J22): buildEnvFromSpec used to fabricate
// a throwaway *ClaudeCLI just to call its private buildEnv method — two spec
// systems for one subprocess. ClaudeCLI is gone; this is the only path.
func buildProcessEnv(resolved config.ResolvedModel, small *config.ResolvedModel) ([]string, error) {
	modelEnv, err := BuildModelEnv(resolved, small)
	if err != nil {
		return nil, fmt.Errorf("build model env: %w", err)
	}

	blocked := []string{
		"ANTHROPIC_API_KEY=",
		"CLAUDE_CODE_SESSION_ID=",
		"CLAUDE_CODE_CHILD_SESSION=",
	}
	var clean []string
	for _, kv := range os.Environ() {
		filtered := false
		for _, prefix := range blocked {
			if strings.HasPrefix(kv, prefix) {
				filtered = true
				break
			}
		}
		if !filtered {
			clean = append(clean, kv)
		}
	}
	return append(clean, modelEnv...), nil
}

// buildModelEnvFromSpec returns only the model-routing env vars derived from spec.Model.
// Used to inject ANTHROPIC_BASE_URL/ANTHROPIC_MODEL into the sandbox HarnessEnv so that
// sandboxed processes can reach the configured model regardless of spec.Sandbox.Env.
// Returns nil, nil for native-provider specs (no env override needed).
func buildModelEnvFromSpec(spec ProcessSpec) ([]string, error) {
	resolved, utility := resolvedModelFromSpec(spec)
	return BuildModelEnv(resolved, utility)
}

// buildEnvFromSpec constructs the subprocess environment from a ProcessSpec.
func buildEnvFromSpec(spec ProcessSpec) ([]string, error) {
	resolved, utility := resolvedModelFromSpec(spec)
	return buildProcessEnv(resolved, utility)
}

// resolvedModelFromSpec converts a ProcessSpec's harness-internal ModelSpec
// back into config.ResolvedModel values for the env builders above.
func resolvedModelFromSpec(spec ProcessSpec) (resolved config.ResolvedModel, utility *config.ResolvedModel) {
	resolved = config.ResolvedModel{
		Type:    spec.Model.Provider,
		Model:   spec.Model.Model,
		BaseURL: spec.Model.BaseURL,
		APIKey:  spec.Model.APIKey,
	}
	if spec.Model.SmallModel != "" {
		u := config.ResolvedModel{
			Type:    spec.Model.Provider,
			Model:   spec.Model.SmallModel,
			BaseURL: spec.Model.BaseURL,
		}
		utility = &u
	}
	return resolved, utility
}
