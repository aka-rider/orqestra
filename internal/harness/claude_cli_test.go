package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
)

func TestBuildEnv_Anthropic(t *testing.T) {
	// INV-P5-ROUTE: anthropic provider sets correct ANTHROPIC_* env vars for the subprocess
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost:4141",
		APIKey:  "test-key",
		Model:   "claude-sonnet-4.6",
		Type:    config.ProviderTypeAnthropic,
	})

	env, err := cli.buildEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvContains(t, env, "ANTHROPIC_BASE_URL=http://localhost:4141")
	assertEnvContains(t, env, "ANTHROPIC_MODEL=claude-sonnet-4.6")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4.6")
	assertEnvNotContains(t, env, "ANTHROPIC_AUTH_TOKEN")
	assertEnvNotContains(t, env, "ANTHROPIC_API_KEY")
}

func TestBuildEnv_OpenAI(t *testing.T) {
	// INV-P5-ROUTE: openai provider sets correct ANTHROPIC_* passthrough env vars
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://192.168.50.212:11434",
		APIKey:  "sk-test",
		Model:   "qwen36",
		Type:    config.ProviderTypeOpenAI,
	})

	env, err := cli.buildEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvContains(t, env, "ANTHROPIC_BASE_URL=http://192.168.50.212:11434")
	assertEnvContains(t, env, "ANTHROPIC_MODEL=qwen36")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_SONNET_MODEL=qwen36")
	assertEnvNotContains(t, env, "ANTHROPIC_API_KEY")
	assertEnvNotContains(t, env, "ANTHROPIC_AUTH_TOKEN")
}

func TestBuildEnv_EmptyType_Errors(t *testing.T) {
	// INV-P5-ROUTE + INV-P5-FAILCLOSED: unknown provider type must error at subprocess-build time
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost:11434",
		Model:   "qwen36",
		Type:    "",
	})

	_, err := cli.buildEnv()
	if err == nil {
		t.Fatal("expected error for empty provider type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider type") {
		t.Errorf("error = %q, want it to contain 'unknown provider type'", err)
	}
}

func TestBuildEnv_UnknownType_Errors(t *testing.T) {
	// INV-P5-ROUTE + INV-P5-FAILCLOSED: unrecognized provider type must error, never silently default
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost:11434",
		Model:   "qwen36",
		Type:    "copilot-proxy",
	})

	_, err := cli.buildEnv()
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

func TestNewClaudeCLIFromConfig_EmptyModelRef(t *testing.T) {
	// INV-P5-FAILCLOSED: empty model_ref is explicit user intent that must error, not default
	cfg := &config.Config{}
	_, err := NewClaudeCLIFromConfig(cfg, "")
	if err == nil {
		t.Fatal("expected error for empty model_ref, got nil")
	}
	if !strings.Contains(err.Error(), "missing model_ref") {
		t.Errorf("error = %q, want it to contain 'missing model_ref'", err)
	}
}

func TestNewClaudeCLIFromConfig_UnknownModelRef(t *testing.T) {
	// INV-P5-FAILCLOSED: unknown model ref must error with the ref name, not silently fall back
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {BaseURL: "http://localhost", APIKey: "k", Type: "openai"},
		},
		Models: map[string]config.ModelConfig{},
	}
	_, err := NewClaudeCLIFromConfig(cfg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown model_ref, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error = %q, want it to contain the model ref 'nonexistent'", err)
	}
}

func TestStreamEventsFrom(t *testing.T) {
	// INV-P4-STREAM: event dispatcher routes each stream event type to the correct Event fields
	cases := []struct {
		name      string
		eventJSON string
		wantText  string
		wantTools []string
	}{
		{
			name:      "content_block_delta writes text",
			eventJSON: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
			wantText:  "hello",
		},
		{
			name:      "assistant text is written",
			eventJSON: `{"type":"assistant","message":{"content":[{"type":"text","text":"thinking..."}]}}`,
			wantText:  "thinking...",
		},
		{
			name:      "assistant tool_use fires OnToolUse",
			eventJSON: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
			wantTools: []string{"Read:main.go"},
		},
		{
			name:      "content_block_start fires OnToolUse",
			eventJSON: `{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}}`,
			wantTools: []string{"Bash:go test ./..."},
		},
		{
			name:      "stream_event wrapping content_block_delta writes text",
			eventJSON: `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"streamed"}}}`,
			wantText:  "streamed",
		},
		{
			name:      "unknown event type is a no-op",
			eventJSON: `{"type":"ping"}`,
			wantText:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var event streamEvent
			if err := json.Unmarshal([]byte(tc.eventJSON), &event); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}

			updates := streamEventsFrom(event)

			var gotText strings.Builder
			var gotTools []string
			for _, u := range updates {
				if u.Text != "" {
					gotText.WriteString(u.Text)
				}
				if u.Tool != "" {
					gotTools = append(gotTools, u.Tool+":"+u.Detail)
				}
			}

			if got := gotText.String(); got != tc.wantText {
				t.Errorf("text: got %q, want %q", got, tc.wantText)
			}
			if len(tc.wantTools) == 0 && len(gotTools) != 0 {
				t.Errorf("unexpected tool calls: %v", gotTools)
			}
			for i, want := range tc.wantTools {
				if i >= len(gotTools) {
					t.Errorf("missing tool call[%d]: want %q", i, want)
					continue
				}
				if gotTools[i] != want {
					t.Errorf("tool[%d]: got %q, want %q", i, gotTools[i], want)
				}
			}
		})
	}
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
