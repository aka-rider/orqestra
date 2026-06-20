package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
)

func TestBuildEnv_Anthropic(t *testing.T) {
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

func TestBuildEnv_Anthropic_WithSmallModel(t *testing.T) {
	small := config.ResolvedModel{
		Model: "claude-haiku",
	}
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost:4141",
		APIKey:  "key",
		Model:   "claude-sonnet-4.6",
		Type:    config.ProviderTypeAnthropic,
	}, WithSmallModel(small))

	env, err := cli.buildEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvContains(t, env, "ANTHROPIC_SMALL_FAST_MODEL=claude-haiku")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-haiku")
}

func TestBuildEnv_OpenAI(t *testing.T) {
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

func TestBuildEnv_OpenAI_WithSmallModel(t *testing.T) {
	small := config.ResolvedModel{Model: "qwen36-fast"}
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://192.168.50.212:11434",
		Model:   "qwen36",
		Type:    config.ProviderTypeOpenAI,
	}, WithSmallModel(small))

	env, err := cli.buildEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvContains(t, env, "ANTHROPIC_SMALL_FAST_MODEL=qwen36-fast")
	assertEnvContains(t, env, "ANTHROPIC_DEFAULT_HAIKU_MODEL=qwen36-fast")
}

func TestBuildEnv_NoOperationalFlags(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{
		BaseURL: "http://localhost",
		Type:    config.ProviderTypeAnthropic,
	})

	env, err := cli.buildEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvNotContains(t, env, "DISABLE_NON_ESSENTIAL_MODEL_CALLS")
	assertEnvNotContains(t, env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
}

func TestBuildEnv_EmptyType_Errors(t *testing.T) {
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

func TestBuildModelEnv_OpenAI_TrimsTrailingSlash(t *testing.T) {
	env, err := BuildModelEnv(config.ResolvedModel{
		BaseURL: "http://localhost:11434/",
		Model:   "qwen36",
		Type:    config.ProviderTypeOpenAI,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEnvContains(t, env, "ANTHROPIC_BASE_URL=http://localhost:11434")
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

	cli, err := NewClaudeCLIFromConfig(cfg, "worker", WithExtraArgs("--verbose-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	cli := NewClaudeCLI(config.ResolvedModel{Type: config.ProviderTypeAnthropic},
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
	cli := NewClaudeCLI(config.ResolvedModel{Type: config.ProviderTypeAnthropic},
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

func TestStreamEventsFrom(t *testing.T) {
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

func TestBuildFinalArgs_NoAutoDisallowAskUserQuestion(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{Type: config.ProviderTypeAnthropic},
		WithDisallowedTools([]string{"ExitPlanMode"}),
		WithInlineMCPServer("orqestra", "/usr/bin/orqestra", []string{"mcp-bridge"}),
	)

	args := cli.buildFinalArgs()

	for i, arg := range args {
		if arg == "--disallowedTools" && i+1 < len(args) {
			if strings.Contains(args[i+1], "AskUserQuestion") {
				t.Error("buildFinalArgs should NOT auto-disallow AskUserQuestion")
			}
			break
		}
	}
}

func TestWithAppendSystemPrompt(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{Type: config.ProviderTypeAnthropic},
		WithAppendSystemPrompt("Use the MCP tool for questions."),
	)

	if cli.appendSystemPrompt != "Use the MCP tool for questions." {
		t.Errorf("appendSystemPrompt = %q, want %q", cli.appendSystemPrompt, "Use the MCP tool for questions.")
	}
}

func TestMergeAppendPrompts(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{"both non-empty", []string{"role rules", "bridge nudge"}, "role rules\n\nbridge nudge"},
		{"first empty", []string{"", "bridge nudge"}, "bridge nudge"},
		{"second empty", []string{"role rules", ""}, "role rules"},
		{"both empty", []string{"", ""}, ""},
		{"whitespace only", []string{"  ", "\n"}, ""},
		{"three parts", []string{"a", "b", "c"}, "a\n\nb\n\nc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeAppendPrompts(tt.parts...)
			if got != tt.want {
				t.Errorf("MergeAppendPrompts(%v) = %q, want %q", tt.parts, got, tt.want)
			}
		})
	}
}

// TestNoSystemPromptFlag verifies that ClaudeCLI never emits --system-prompt.
// All system prompt steering goes through --append-system-prompt (layer 5) to
// preserve CLAUDE.md and the default prompt (layer 4).
func TestNoSystemPromptFlag(t *testing.T) {
	// Provide both a role system prompt (via RunPrint parameter) and an
	// append option to verify they are merged into a single --append-system-prompt.
	// We cannot call RunPrint without a real binary, but we can verify the
	// MergeAppendPrompts helper produces the right combined text and that
	// buildFinalArgs never contains --system-prompt.
	cli := NewClaudeCLI(config.ResolvedModel{Type: config.ProviderTypeAnthropic},
		WithAppendSystemPrompt("bridge nudge"),
		WithExtraArgs("--permission-mode", "plan"),
	)

	args := cli.buildFinalArgs()
	for _, arg := range args {
		if arg == "--system-prompt" {
			t.Error("buildFinalArgs must not contain --system-prompt; all steering goes through --append-system-prompt")
		}
	}
}

func TestWithAllowedTools_FlagName(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{Type: config.ProviderTypeAnthropic},
		WithAllowedTools([]string{"Read", "mcp__orqestra__AskUserQuestion"}),
	)

	args := cli.buildFinalArgs()
	for i, arg := range args {
		if arg == "--allowedTools" && i+1 < len(args) {
			if args[i+1] != "Read,mcp__orqestra__AskUserQuestion" {
				t.Errorf("--allowedTools value = %q, want %q", args[i+1], "Read,mcp__orqestra__AskUserQuestion")
			}
			return
		}
	}
	t.Error("expected --allowedTools in args")
}

func TestWithDisallowedTools_FlagName(t *testing.T) {
	cli := NewClaudeCLI(config.ResolvedModel{Type: config.ProviderTypeAnthropic},
		WithDisallowedTools([]string{"AskUserQuestion", "ExitPlanMode"}),
	)

	args := cli.buildFinalArgs()
	for i, arg := range args {
		if arg == "--disallowedTools" && i+1 < len(args) {
			if args[i+1] != "AskUserQuestion,ExitPlanMode" {
				t.Errorf("--disallowedTools value = %q, want %q", args[i+1], "AskUserQuestion,ExitPlanMode")
			}
			return
		}
	}
	t.Error("expected --disallowedTools in args")
}

// TestPipeModeToolPreApproval verifies the CLI arg patterns required for
// Orqestra's pipe-mode constraints:
//   - mcp__orqestra__AskUserQuestion must always be pre-approved
//   - Wildcards ("*", "mcp__*") must be present for full tool pre-approval
//   - Built-in AskUserQuestion must be in disallowed (replaced by MCP bridge)
//   - --strict-mcp-config controls which servers start (context window)
//   - AllowedTools/DisallowedTools must be set even with --strict-mcp-config
func TestPipeModeToolPreApproval(t *testing.T) {
	resolved := config.ResolvedModel{Type: config.ProviderTypeAnthropic}

	tests := []struct {
		name             string
		opts             []ClaudeCLIOption
		wantAllowed      string // substring expected in --allowedTools value
		wantDisallowed   string // substring expected in --disallowedTools value
		wantStrict       bool   // expect --strict-mcp-config
		wantNoAllowed    bool   // expect NO --allowedTools (mutually exclusive with wantAllowed)
		wantNoDisallowed bool   // expect NO --disallowedTools
	}{
		{
			name: "plan mode with wildcards and bridge tool",
			opts: []ClaudeCLIOption{
				WithPermissionMode("plan"),
				WithAllowedTools([]string{"*", "mcp__*", "mcp__orqestra__AskUserQuestion"}),
				WithDisallowedTools([]string{"AskUserQuestion", "ExitPlanMode"}),
			},
			wantAllowed:    "mcp__orqestra__AskUserQuestion",
			wantDisallowed: "AskUserQuestion",
		},
		{
			name: "strict MCP + allowed tools coexist",
			opts: []ClaudeCLIOption{
				WithPermissionMode("plan"),
				WithMCPServers([]string{"mcp_docker"}),
				WithAllowedTools([]string{"*", "mcp__*", "mcp__orqestra__AskUserQuestion"}),
				WithDisallowedTools([]string{"AskUserQuestion"}),
				WithInlineMCPServer("orqestra", "/bin/orqestra", []string{"mcp-bridge"}),
			},
			wantAllowed:    "mcp__orqestra__AskUserQuestion",
			wantDisallowed: "AskUserQuestion",
			wantStrict:     true,
		},
		{
			name: "no-tools mode still gets allowed for bridge",
			opts: []ClaudeCLIOption{
				WithPermissionMode("plan"),
				WithNoTools(),
				WithAllowedTools([]string{"mcp__orqestra__AskUserQuestion"}),
				WithInlineMCPServer("orqestra", "/bin/orqestra", []string{"mcp-bridge"}),
			},
			wantAllowed: "mcp__orqestra__AskUserQuestion",
			wantStrict:  true,
		},
		{
			name: "wildcards cover all built-in and MCP tools",
			opts: []ClaudeCLIOption{
				WithAllowedTools([]string{"*", "mcp__*", "mcp__orqestra__AskUserQuestion"}),
			},
			wantAllowed: "*,mcp__*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := NewClaudeCLI(resolved, tt.opts...)
			args := cli.buildFinalArgs()

			allowedVal := flagValue(args, "--allowedTools")
			disallowedVal := flagValue(args, "--disallowedTools")
			hasStrict := false
			for _, arg := range args {
				if arg == "--strict-mcp-config" {
					hasStrict = true
					break
				}
			}

			if tt.wantNoAllowed {
				if allowedVal != "" {
					t.Errorf("expected no --allowedTools, got %q", allowedVal)
				}
			} else if tt.wantAllowed != "" {
				if !strings.Contains(allowedVal, tt.wantAllowed) {
					t.Errorf("--allowedTools = %q, want substring %q", allowedVal, tt.wantAllowed)
				}
			}

			if tt.wantNoDisallowed {
				if disallowedVal != "" {
					t.Errorf("expected no --disallowedTools, got %q", disallowedVal)
				}
			} else if tt.wantDisallowed != "" {
				if !strings.Contains(disallowedVal, tt.wantDisallowed) {
					t.Errorf("--disallowedTools = %q, want substring %q", disallowedVal, tt.wantDisallowed)
				}
			}

			if tt.wantStrict != hasStrict {
				t.Errorf("--strict-mcp-config present = %v, want %v", hasStrict, tt.wantStrict)
			}
		})
	}
}

func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
