package harness

import (
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
)

func TestBuildEnv_Anthropic(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost:4141",
		APIKey:  "test-key",
		Model:   "claude-sonnet-4.6",
		Type:    "anthropic",
	})

	env := cli.buildEnv()
	assertEnvContains(t, env, "ANTHROPIC_BASE_URL=http://localhost:4141")
	assertEnvContains(t, env, "ANTHROPIC_AUTH_TOKEN=test-key")
	assertEnvContains(t, env, "ANTHROPIC_MODEL=claude-sonnet-4.6")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4.6")
}

func TestBuildEnv_Anthropic_WithSmallModel(t *testing.T) {
	small := config.ResolvedModel{
		Model: "claude-haiku",
	}
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost:4141",
		APIKey:  "key",
		Model:   "claude-sonnet-4.6",
		Type:    "anthropic",
	}, WithSmallModel(small))

	env := cli.buildEnv()
	assertEnvContains(t, env, "ANTHROPIC_SMALL_FAST_MODEL=claude-haiku")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-haiku")
}

func TestBuildEnv_OpenAI(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://192.168.50.212:11434",
		APIKey:  "sk-test",
		Model:   "qwen36",
		Type:    "openai",
	})

	env := cli.buildEnv()
	assertEnvContains(t, env, "OPENAI_BASE_URL=http://192.168.50.212:11434/v1")
	assertEnvContains(t, env, "OPENAI_API_KEY=sk-test")
}

func TestBuildEnv_OperationalFlags(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost",
		Type:    "anthropic",
	})

	env := cli.buildEnv()
	assertEnvContains(t, env, "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
	assertEnvContains(t, env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
}

func TestBuildEnv_OperationalFlags_OpenAI(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost",
		Type:    "openai",
	})

	env := cli.buildEnv()
	assertEnvContains(t, env, "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
	assertEnvContains(t, env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
}

func assertEnvContains(t *testing.T, env []string, expected string) {
	t.Helper()
	for _, e := range env {
		if e == expected {
			return
		}
	}
	// Show only relevant env vars for debugging
	var relevant []string
	prefix := strings.Split(expected, "=")[0]
	for _, e := range env {
		if strings.HasPrefix(e, prefix) || strings.HasPrefix(e, "ANTHROPIC") || strings.HasPrefix(e, "OPENAI") || strings.HasPrefix(e, "DISABLE") || strings.HasPrefix(e, "CLAUDE_CODE") {
			relevant = append(relevant, e)
		}
	}
	t.Errorf("env does not contain %q\nrelevant vars: %v", expected, relevant)
}
