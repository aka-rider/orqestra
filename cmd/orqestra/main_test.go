package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

func TestRun_InvalidFlag(t *testing.T) {
	outStream := new(bytes.Buffer)
	errStream := new(bytes.Buffer)
	args := []string{"orqestra", "--unknown-flag"}

	exitCode := run(args, outStream, errStream)
	if exitCode != exitInvalidInput {
		t.Fatalf("expected exitInvalidInput (2), got %d. stderr: %s", exitCode, errStream.String())
	}
}

func TestRun_MissingConfig(t *testing.T) {
	outStream := new(bytes.Buffer)
	errStream := new(bytes.Buffer)
	args := []string{"orqestra", "--config", "nonexistent-config.yaml", "usage"}

	exitCode := run(args, outStream, errStream)
	if exitCode != exitUserCancelled {
		t.Fatalf("expected exitUserCancelled (130) for non-tty InitGate, got %d. stderr: %s", exitCode, errStream.String())
	}
}

func TestRun_Help(t *testing.T) {
	outStream := new(bytes.Buffer)
	errStream := new(bytes.Buffer)
	args := []string{"orqestra"}

	exitCode := run(args, outStream, errStream)
	if exitCode != exitUserCancelled {
		t.Fatalf("expected exitUserCancelled (130) for non-tty InitGate, got %d", exitCode)
	}
}

// TestBridgeToolOpts_Constraints validates the CLI argument patterns produced the CLI argument patterns produced
// by bridgeToolOpts against Orqestra's hard constraints:
//
//  1. mcp__orqestra__AskUserQuestion is ALWAYS in --allowedTools
//  2. mcp__* is in --allowedTools for MCP bridge tools; no bare "*" (least-privilege)
//  3. Built-in AskUserQuestion is ALWAYS in --disallowedTools
//  4. When mcp_servers is explicit, --strict-mcp-config is set
//  5. When mcp_servers is nil, no --strict-mcp-config (all user MCPs available)
//  6. --allowedTools and --disallowedTools are set even with --strict-mcp-config
//  7. --settings injects permissions.allow ["mcp__*"] for all MCP tools
func TestBridgeToolOpts_Constraints(t *testing.T) {
	// INV-P2-WRITE: worker never receives bare "*" tool grant; least-privilege MCP constraints enforced
	mcpDocker := []string{"mcp_docker", "orqestra"}
	emptyMCP := []string{}

	tests := []struct {
		name       string
		base       config.BaseAgentConfig
		wantStrict bool // expect --strict-mcp-config
	}{
		{
			name: "nil mcp_servers — all user MCPs, permissive",
			base: config.BaseAgentConfig{
				PermissionMode:  "plan",
				DisallowedTools: []string{"AskUserQuestion", "ExitPlanMode"},
				MCPServers:      nil,
			},
			wantStrict: false,
		},
		{
			name: "explicit mcp_servers — strict filtering for context savings",
			base: config.BaseAgentConfig{
				PermissionMode:  "plan",
				DisallowedTools: []string{"AskUserQuestion", "ExitPlanMode"},
				MCPServers:      &mcpDocker,
			},
			wantStrict: true,
		},
		{
			name: "empty mcp_servers — no user MCPs, bridge still pre-approved",
			base: config.BaseAgentConfig{
				PermissionMode:  "plan",
				DisallowedTools: []string{"AskUserQuestion"},
				MCPServers:      &emptyMCP,
			},
			wantStrict: true,
		},
		{
			name: "full permission mode — wildcards still injected for pipe mode",
			base: config.BaseAgentConfig{
				PermissionMode:  "full",
				DisallowedTools: []string{"AskUserQuestion"},
				MCPServers:      nil,
			},
			wantStrict: false,
		},
		{
			name: "no explicit disallowed — AskUserQuestion auto-added",
			base: config.BaseAgentConfig{
				PermissionMode: "plan",
				MCPServers:     nil,
			},
			wantStrict: false,
		},
		{
			name: "user-supplied allowed_tools preserved alongside wildcards",
			base: config.BaseAgentConfig{
				PermissionMode: "plan",
				AllowedTools:   []string{"Read", "Grep"},
				MCPServers:     nil,
			},
			wantStrict: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := bridgeToolOpts(tt.base)
			args := harness.SpecArgsFromOptions(opts...)

			allowed := flagValue(args, "--allowedTools")
			disallowed := flagValue(args, "--disallowedTools")

			// CONSTRAINT 1: mcp__orqestra__AskUserQuestion always pre-approved
			if !strings.Contains(allowed, "mcp__orqestra__AskUserQuestion") {
				t.Errorf("CONSTRAINT 1 violated: --allowedTools = %q, must contain mcp__orqestra__AskUserQuestion", allowed)
			}

			// CONSTRAINT 2: mcp__* for bridge tool pre-approval; bare "*" forbidden (least-privilege)
			if !strings.Contains(allowed, "mcp__*") {
				t.Errorf("CONSTRAINT 2 violated: --allowedTools = %q, must contain wildcard mcp__*", allowed)
			}
			// Bare "*" would grant every built-in tool — must not be present.
			for _, part := range strings.Split(allowed, ",") {
				if strings.TrimSpace(part) == "*" {
					t.Errorf("CONSTRAINT 2 (least-privilege) violated: --allowedTools contains bare \"*\": %q", allowed)
				}
			}

			// CONSTRAINT 3: built-in AskUserQuestion always blocked
			if !strings.Contains(disallowed, "AskUserQuestion") {
				t.Errorf("CONSTRAINT 3 violated: --disallowedTools = %q, must contain AskUserQuestion", disallowed)
			}

			// CONSTRAINT 4: --strict-mcp-config presence
			hasStrict := false
			for _, arg := range args {
				if arg == "--strict-mcp-config" {
					hasStrict = true
					break
				}
			}
			if hasStrict != tt.wantStrict {
				t.Errorf("CONSTRAINT 4/5: --strict-mcp-config = %v, want %v", hasStrict, tt.wantStrict)
			}

			// CONSTRAINT 6: if explicit mcp_servers, allowed/disallowed still present
			if tt.wantStrict {
				if allowed == "" {
					t.Error("CONSTRAINT 6 violated: --allowedTools missing with --strict-mcp-config")
				}
			}

			// CONSTRAINT 7: --settings injects permissions.allow for all MCP tools (mcp__*)
			settings := flagValue(args, "--settings")
			if !strings.Contains(settings, `"mcp__*"`) {
				t.Errorf("CONSTRAINT 7 violated: --settings = %q, must contain mcp__* permission", settings)
			}

			// Verify user-supplied allowed_tools are preserved
			if len(tt.base.AllowedTools) > 0 {
				for _, tool := range tt.base.AllowedTools {
					if !strings.Contains(allowed, tool) {
						t.Errorf("user allowed_tool %q lost from --allowedTools = %q", tool, allowed)
					}
				}
			}
		})
	}
}

// TestLeastPrivilege_NoWildcardStar verifies that read-only roles (researcher,
// architect, critic) never get a bare "*" tool grant regardless of what is
// in AllowedTools — the configured list is used verbatim plus mcp__*.
func TestLeastPrivilege_NoWildcardStar(t *testing.T) {
	// INV-P2-WRITE: read-only roles never receive bare "*" in --allowedTools
	roleAllowedTools := [][]string{
		// researcher
		{"Read", "Glob", "Grep", "Bash", "WebFetch", "WebSearch", "mcp__orqestra__*"},
		// architect
		{"Read", "Glob", "Grep", "Bash", "WebFetch", "WebSearch", "mcp__orqestra__*"},
		// critic
		{"Read", "Glob", "Grep", "Bash", "WebSearch", "mcp__orqestra__*"},
	}

	for _, allowed := range roleAllowedTools {
		base := config.BaseAgentConfig{
			AllowedTools:    allowed,
			DisallowedTools: []string{"AskUserQuestion"},
		}
		opts := bridgeToolOpts(base)
		args := harness.SpecArgsFromOptions(opts...)
		allowedStr := flagValue(args, "--allowedTools")

		for _, part := range strings.Split(allowedStr, ",") {
			if strings.TrimSpace(part) == "*" {
				t.Errorf("least-privilege violation: bare \"*\" in --allowedTools for role with allowed=%v: %q",
					allowed, allowedStr)
			}
		}
		// All role tools must be present.
		for _, tool := range allowed {
			if !strings.Contains(allowedStr, tool) {
				t.Errorf("role tool %q lost from --allowedTools = %q", tool, allowedStr)
			}
		}
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
