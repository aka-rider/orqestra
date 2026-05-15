package harness

import (
	"encoding/json"
	"io"
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
	assertEnvContains(t, env, "ANTHROPIC_MODEL=claude-sonnet-4.6")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4.6")
	assertEnvNotContains(t, env, "ANTHROPIC_AUTH_TOKEN")
	assertEnvNotContains(t, env, "ANTHROPIC_API_KEY")
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
	assertEnvContains(t, env, "ANTHROPIC_BASE_URL=http://192.168.50.212:11434")
	assertEnvContains(t, env, "ANTHROPIC_MODEL=qwen36")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_SONNET_MODEL=qwen36")
	assertEnvNotContains(t, env, "ANTHROPIC_API_KEY")
	assertEnvNotContains(t, env, "ANTHROPIC_AUTH_TOKEN")
}

func TestBuildEnv_OpenAI_WithSmallModel(t *testing.T) {
	small := config.ResolvedModel{Model: "qwen36-fast"}
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://192.168.50.212:11434",
		Model:   "qwen36",
		Type:    "openai",
	}, WithSmallModel(small))

	env := cli.buildEnv()
	assertEnvContains(t, env, "ANTHROPIC_SMALL_FAST_MODEL=qwen36-fast")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_HAIKU_MODEL=qwen36-fast")
}

func TestBuildEnv_NoOperationalFlags(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost",
		Type:    "anthropic",
	})

	env := cli.buildEnv()
	assertEnvNotContains(t, env, "DISABLE_NON_ESSENTIAL_MODEL_CALLS")
	assertEnvNotContains(t, env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
}

func TestNewClaudeCLIFromConfig_AppliesModelRuntimeOptions(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {BaseURL: "http://localhost:11434", APIKey: "key", Type: "openai"},
		},
		Models: map[string]config.ModelConfig{
			"small": {Provider: "local", Model: "qwen36-fast"},
			"worker": {
				Provider: "local",
				Model:    "qwen36",
				Binary:   "claude-test",
			},
		},
	}

	runner, err := NewClaudeCLIFromConfig(cfg, "worker", WithExtraArgs("--verbose-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cli, ok := runner.(*ClaudeCLI)
	if !ok {
		t.Fatalf("runner = %T, want *ClaudeCLI", runner)
	}
	if cli.binary != "claude-test" {
		t.Errorf("binary = %q, want claude-test", cli.binary)
	}
	if cli.small == nil || cli.small.Model != "qwen36-fast" {
		t.Fatalf("small model = %+v, want qwen36-fast", cli.small)
	}
	expectedArgs := []string{"--verbose-mode"}
	if strings.Join(cli.extraArgs, ",") != strings.Join(expectedArgs, ",") {
		t.Errorf("extraArgs = %v, want %v", cli.extraArgs, expectedArgs)
	}
}

func TestNewClaudeCLIFromConfig_EmptyModelRef(t *testing.T) {
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

func assertEnvNotContains(t *testing.T, env []string, prefix string) {
	t.Helper()
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			t.Errorf("env should not contain %q but found: %s", prefix, e)
		}
	}
}

func TestBuildFinalArgs_InlineOnly_NoStrictMCP(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{Type: "anthropic"},
		WithInlineMCPServer("orqestra", "/usr/bin/orqestra", []string{"mcp-bridge"}),
	)

	args := cli.buildFinalArgs()

	for _, arg := range args {
		if arg == "--strict-mcp-config" {
			t.Error("inline-only MCP should NOT add --strict-mcp-config")
		}
	}

	found := false
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			found = true
			var cfg struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil {
				t.Fatalf("failed to parse --mcp-config JSON: %v", err)
			}
			if _, ok := cfg.MCPServers["orqestra"]; !ok {
				t.Error("--mcp-config missing 'orqestra' server")
			}
			break
		}
	}
	if !found {
		t.Error("expected --mcp-config in args")
	}
}

func TestBuildFinalArgs_InlineWithStrict_KeepsStrict(t *testing.T) {
	// Simulate what WithMCPServers produces: --strict-mcp-config + --mcp-config with servers
	cli := NewClaudeCLI(config.ResolvedModel{Type: "anthropic"},
		WithExtraArgs("--strict-mcp-config", "--mcp-config", `{"mcpServers":{"context7":{"command":"npx","args":["context7"]}}}`),
		WithInlineMCPServer("orqestra", "/usr/bin/orqestra", []string{"mcp-bridge"}),
	)

	args := cli.buildFinalArgs()

	hasStrict := false
	for _, arg := range args {
		if arg == "--strict-mcp-config" {
			hasStrict = true
		}
	}
	if !hasStrict {
		t.Error("expected --strict-mcp-config to be preserved when explicitly set")
	}

	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			var cfg struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil {
				t.Fatalf("failed to parse --mcp-config JSON: %v", err)
			}
			if _, ok := cfg.MCPServers["orqestra"]; !ok {
				t.Error("--mcp-config should contain merged 'orqestra' server")
			}
			if _, ok := cfg.MCPServers["context7"]; !ok {
				t.Error("--mcp-config should preserve existing 'context7' server")
			}
			break
		}
	}
}

// mockSink captures tool-use notifications for test assertions.
type mockSink struct {
	buf   strings.Builder
	tools []string
}

func (m *mockSink) Write(p []byte) (int, error) {
	return m.buf.Write(p)
}

func (m *mockSink) OnToolUse(name, detail string) {
	m.tools = append(m.tools, name+":"+detail)
}

func TestDispatchStreamEvent(t *testing.T) {
	cases := []struct {
		name       string
		eventJSON  string
		nilDisplay bool
		wantText   string
		wantTools  []string
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
		{
			name:       "nil display does not panic",
			eventJSON:  `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
			nilDisplay: true,
			wantText:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var event streamEvent
			if err := json.Unmarshal([]byte(tc.eventJSON), &event); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}

			var sink *mockSink
			var display io.Writer
			if tc.nilDisplay {
				display = nil
			} else {
				sink = &mockSink{}
				display = sink
			}

			dispatchStreamEvent(event, display) // must not panic

			if sink == nil {
				return // nil display case; just verifying no panic
			}
			if got := sink.buf.String(); got != tc.wantText {
				t.Errorf("text: got %q, want %q", got, tc.wantText)
			}
			if len(tc.wantTools) == 0 && len(sink.tools) != 0 {
				t.Errorf("unexpected tool calls: %v", sink.tools)
			}
			for i, want := range tc.wantTools {
				if i >= len(sink.tools) {
					t.Errorf("missing tool call[%d]: want %q", i, want)
					continue
				}
				if sink.tools[i] != want {
					t.Errorf("tool[%d]: got %q, want %q", i, sink.tools[i], want)
				}
			}
		})
	}
}

func TestBuildFinalArgs_NoAutoDisallowAskUserQuestion(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{Type: "anthropic"},
		WithDisallowedTools([]string{"ExitPlanMode"}),
		WithInlineMCPServer("orqestra", "/usr/bin/orqestra", []string{"mcp-bridge"}),
	)

	args := cli.buildFinalArgs()

	for i, arg := range args {
		if arg == "--disallowed-tools" && i+1 < len(args) {
			if strings.Contains(args[i+1], "AskUserQuestion") {
				t.Error("buildFinalArgs should NOT auto-disallow AskUserQuestion")
			}
			break
		}
	}
}
