package harness

import (
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
)

func TestBuildModelEnv_Anthropic(t *testing.T) {
	// INV-P5-ROUTE: anthropic provider sets correct ANTHROPIC_* env vars for the subprocess
	env, err := BuildModelEnv(config.ResolvedModel{
		BaseURL: "http://localhost:4141",
		APIKey:  "test-key",
		Model:   "claude-sonnet-4.6",
		Type:    config.ProviderTypeAnthropic,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvContains(t, env, "ANTHROPIC_BASE_URL=http://localhost:4141")
	assertEnvContains(t, env, "ANTHROPIC_MODEL=claude-sonnet-4.6")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4.6")
	// The configured api_key must be emitted so the subprocess authenticates
	// against the configured endpoint instead of falling back to subscription OAuth.
	assertEnvContains(t, env, "ANTHROPIC_API_KEY=test-key")
	assertEnvNotContains(t, env, "ANTHROPIC_AUTH_TOKEN")
}

func TestBuildModelEnv_OpenAI(t *testing.T) {
	// INV-P5-ROUTE: openai provider sets correct ANTHROPIC_* passthrough env vars
	env, err := BuildModelEnv(config.ResolvedModel{
		BaseURL: "http://192.168.50.212:11434",
		APIKey:  "sk-test",
		Model:   "qwen36",
		Type:    config.ProviderTypeOpenAI,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvContains(t, env, "ANTHROPIC_BASE_URL=http://192.168.50.212:11434")
	assertEnvContains(t, env, "ANTHROPIC_MODEL=qwen36")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_SONNET_MODEL=qwen36")
	assertEnvContains(t, env, "ANTHROPIC_API_KEY=sk-test")
	assertEnvNotContains(t, env, "ANTHROPIC_AUTH_TOKEN")
}

// TestBuildModelEnv_KeylessEmitsSentinel verifies that a non-native provider with
// no configured api_key still emits a non-empty ANTHROPIC_API_KEY (the local
// sentinel). This is the core billing fix: a non-empty key forces the claude CLI
// to authenticate against the configured ANTHROPIC_BASE_URL instead of silently
// falling back to the on-disk subscription OAuth (which would bill api.anthropic.com).
func TestBuildModelEnv_KeylessEmitsSentinel(t *testing.T) {
	for _, typ := range []string{config.ProviderTypeOpenAI, config.ProviderTypeAnthropic} {
		t.Run(typ, func(t *testing.T) {
			env, err := BuildModelEnv(config.ResolvedModel{
				BaseURL: "http://192.168.50.212:11434",
				APIKey:  "", // keyless local endpoint (e.g. Ollama)
				Model:   "qwen3.6",
				Type:    typ,
			}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertEnvContains(t, env, "ANTHROPIC_API_KEY="+localAuthSentinel)
			// Sentinel must be non-empty, else the CLI falls back to OAuth.
			if localAuthSentinel == "" {
				t.Fatal("localAuthSentinel must be non-empty to prevent OAuth fallback")
			}
		})
	}
}

func TestBuildModelEnv_EmptyType_Errors(t *testing.T) {
	// INV-P5-ROUTE + INV-P5-FAILCLOSED: unknown provider type must error at subprocess-build time
	_, err := BuildModelEnv(config.ResolvedModel{
		BaseURL: "http://localhost:11434",
		Model:   "qwen36",
		Type:    "",
	}, nil)
	if err == nil {
		t.Fatal("expected error for empty provider type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider type") {
		t.Errorf("error = %q, want it to contain 'unknown provider type'", err)
	}
}

func TestBuildModelEnv_UnknownType_Errors(t *testing.T) {
	// INV-P5-ROUTE + INV-P5-FAILCLOSED: unrecognized provider type must error, never silently default
	_, err := BuildModelEnv(config.ResolvedModel{
		BaseURL: "http://localhost:11434",
		Model:   "qwen36",
		Type:    "copilot-proxy",
	}, nil)
	if err == nil {
		t.Fatal("expected error for unknown provider type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider type") {
		t.Errorf("error = %q, want it to contain 'unknown provider type'", err)
	}
}

func TestBuildModelEnv_Native_ReturnsNilEnv(t *testing.T) {
	// INV-P5-ROUTE: native provider does not inject any env vars (uses ambient Claude auth)
	env, err := BuildModelEnv(config.ResolvedModel{
		Model: "claude-sonnet-4-6",
		Type:  config.ProviderTypeNative,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env != nil {
		t.Errorf("native provider should return nil env, got %v", env)
	}
}

// TestBuildEnvFromSpec_FiltersParentSessionVars proves buildEnvFromSpec (J22:
// the real env builder, no throwaway ClaudeCLI) filters CLAUDE_CODE_SESSION_ID
// / CLAUDE_CODE_CHILD_SESSION / ANTHROPIC_API_KEY from the parent environment
// the same way the pre-WP14 ClaudeCLI.buildEnv did.
func TestBuildEnvFromSpec_FiltersParentSessionVars(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "should-not-leak")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("ANTHROPIC_API_KEY", "should-not-leak-either")

	spec := ProcessSpec{
		Model: ModelSpec{
			Provider: config.ProviderTypeAnthropic,
			Model:    "claude-sonnet-4.6",
			BaseURL:  "http://localhost:4141",
			APIKey:   "configured-key",
		},
	}
	env, err := buildEnvFromSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvNotContains(t, env, "CLAUDE_CODE_SESSION_ID=")
	assertEnvNotContains(t, env, "CLAUDE_CODE_CHILD_SESSION=")
	assertEnvContains(t, env, "ANTHROPIC_API_KEY=configured-key")
}

func assertEnvContains(t *testing.T, env []string, expected string) {
	t.Helper()
	for _, e := range env {
		if e == expected {
			return
		}
	}
	var relevant []string
	prefix := strings.Split(expected, "=")[0]
	for _, e := range env {
		if strings.HasPrefix(e, prefix) || strings.HasPrefix(e, "ANTHROPIC") || strings.HasPrefix(e, "OPENAI") || strings.HasPrefix(e, "DISABLE") || strings.HasPrefix(e, "CLAUDE_CODE") {
			relevant = append(relevant, e)
		}
	}
	t.Errorf("env does not contain %q\nrelevant vars: %v", expected, relevant)
}

func assertEnvNotContains(t *testing.T, env []string, prefix string) {
	t.Helper()
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			t.Errorf("env should not contain %q but found: %s", prefix, e)
		}
	}
}
