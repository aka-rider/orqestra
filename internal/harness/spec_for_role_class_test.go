package harness

import (
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
)

// --- Reporter-class tool constraints (retargeted from cmd/orqestra's former
// TestBridgeToolOpts_Constraints — the pre-WP14 option-function API is gone;
// the same invariants now live on SpecForRole's reporter-class arg builder) ---

// TestSpecForRole_ReporterToolConstraints validates the CLI argument patterns
// produced for a RoleClassReporter role against Orqestra's hard constraints:
//
//  1. mcp__orqestra__AskUserQuestion is ALWAYS in --allowedTools
//  2. mcp__* is in --allowedTools for MCP bridge tools; no bare "*" (least-privilege)
//  3. Built-in AskUserQuestion is ALWAYS in --disallowedTools
//  4. When mcp_servers is explicit, --strict-mcp-config is set
//  5. When mcp_servers is nil, no --strict-mcp-config (all user MCPs available)
//  6. --allowedTools and --disallowedTools are set even with --strict-mcp-config
//  7. --settings injects permissions.allow ["mcp__*"] for all MCP tools
func TestSpecForRole_ReporterToolConstraints(t *testing.T) {
	// INV-P2-WRITE: reporter-class roles never receive a bare "*" tool grant; least-privilege MCP constraints enforced
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
			cfg := fixtureConfig(t)
			base := tt.base
			base.Model = "m"
			cfg.Architect = config.ArchitectConfig{BaseAgentConfig: base}

			spec, err := SpecForRole(cfg, config.RoleSpecInput{Name: "architect"}, SandboxConfig{})
			if err != nil {
				t.Fatalf("SpecForRole: %v", err)
			}
			args := SpecArgs(spec)

			allowed := flagValue(args, "--allowedTools")
			disallowed := flagValue(args, "--disallowedTools")

			if !strings.Contains(allowed, "mcp__orqestra__AskUserQuestion") {
				t.Errorf("CONSTRAINT 1 violated: --allowedTools = %q, must contain mcp__orqestra__AskUserQuestion", allowed)
			}
			if !strings.Contains(allowed, "mcp__*") {
				t.Errorf("CONSTRAINT 2 violated: --allowedTools = %q, must contain wildcard mcp__*", allowed)
			}
			for _, part := range strings.Split(allowed, ",") {
				if strings.TrimSpace(part) == "*" {
					t.Errorf("CONSTRAINT 2 (least-privilege) violated: --allowedTools contains bare \"*\": %q", allowed)
				}
			}
			if !strings.Contains(disallowed, "AskUserQuestion") {
				t.Errorf("CONSTRAINT 3 violated: --disallowedTools = %q, must contain AskUserQuestion", disallowed)
			}

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
			if tt.wantStrict && allowed == "" {
				t.Error("CONSTRAINT 6 violated: --allowedTools missing with --strict-mcp-config")
			}

			settings := flagValue(args, "--settings")
			if !strings.Contains(settings, `"mcp__*"`) {
				t.Errorf("CONSTRAINT 7 violated: --settings = %q, must contain mcp__* permission", settings)
			}

			for _, tool := range tt.base.AllowedTools {
				if !strings.Contains(allowed, tool) {
					t.Errorf("user allowed_tool %q lost from --allowedTools = %q", tool, allowed)
				}
			}
		})
	}
}

// TestSpecForRole_ReporterLeastPrivilege_NoWildcardStar verifies that
// reporter-class roles (architect/critic) never get a bare "*" tool grant
// regardless of what is in AllowedTools — the configured list is used
// verbatim plus mcp__*.
func TestSpecForRole_ReporterLeastPrivilege_NoWildcardStar(t *testing.T) {
	// INV-P2-WRITE: read-only roles never receive bare "*" in --allowedTools
	roleAllowedTools := [][]string{
		{"Read", "Glob", "Grep", "Bash", "WebFetch", "WebSearch", "mcp__orqestra__*"}, // architect-shaped
		{"Read", "Glob", "Grep", "Bash", "WebSearch", "mcp__orqestra__*"},             // critic-shaped
	}

	for _, allowed := range roleAllowedTools {
		cfg := fixtureConfig(t)
		cfg.Architect = config.ArchitectConfig{BaseAgentConfig: config.BaseAgentConfig{
			Model:           "m",
			AllowedTools:    allowed,
			DisallowedTools: []string{"AskUserQuestion"},
		}}

		spec, err := SpecForRole(cfg, config.RoleSpecInput{Name: "architect"}, SandboxConfig{})
		if err != nil {
			t.Fatalf("SpecForRole: %v", err)
		}
		args := SpecArgs(spec)
		allowedStr := flagValue(args, "--allowedTools")

		for _, part := range strings.Split(allowedStr, ",") {
			if strings.TrimSpace(part) == "*" {
				t.Errorf("least-privilege violation: bare \"*\" in --allowedTools for role with allowed=%v: %q",
					allowed, allowedStr)
			}
		}
		for _, tool := range allowed {
			if !strings.Contains(allowedStr, tool) {
				t.Errorf("role tool %q lost from --allowedTools = %q", tool, allowedStr)
			}
		}
	}
}

// --- Executor-class asymmetry (worker never gets reporter-style tool filtering) ---

func TestSpecForRole_ExecutorSkipsToolFiltering(t *testing.T) {
	// Matches the pre-WP14 worker spec exactly: only --permission-mode plus
	// the shared skip-permissions+settings pair. AllowedTools/DisallowedTools/
	// MCPServers configured on the worker role are NOT applied — the worker
	// never routes through the reporter tool-filtering path.
	mcpNames := []string{"some-server"}
	cfg := fixtureConfig(t)
	cfg.Worker = config.WorkerConfig{BaseAgentConfig: config.BaseAgentConfig{
		Model:           "m",
		PermissionMode:  "bypassPermissions",
		AllowedTools:    []string{"Read"},
		DisallowedTools: []string{"Bash"},
		MCPServers:      &mcpNames,
	}}

	spec, err := SpecForRole(cfg, config.RoleSpecInput{Name: "worker"}, SandboxConfig{})
	if err != nil {
		t.Fatalf("SpecForRole: %v", err)
	}
	args := SpecArgs(spec)

	if flagValue(args, "--permission-mode") != "bypassPermissions" {
		t.Errorf("--permission-mode = %q, want bypassPermissions", flagValue(args, "--permission-mode"))
	}
	if flagValue(args, "--allowedTools") != "" {
		t.Errorf("--allowedTools should be absent for an executor-class role, got %q", flagValue(args, "--allowedTools"))
	}
	if flagValue(args, "--disallowedTools") != "" {
		t.Errorf("--disallowedTools should be absent for an executor-class role, got %q", flagValue(args, "--disallowedTools"))
	}
	found := false
	for _, a := range args {
		if a == "--strict-mcp-config" {
			found = true
		}
	}
	if found {
		t.Error("--strict-mcp-config should be absent for an executor-class role")
	}
	if !spec.ExpectsReport || !spec.InputPlane {
		t.Error("executor-class role must expect a report and run interactively")
	}
	if len(spec.Agents) != 0 {
		t.Errorf("executor-class role must not get the researcher subagent, got %+v", spec.Agents)
	}
}

// --- Utility-class shape (integrator's two invocation variants) ---

func TestSpecForRole_UtilityNoToolsOverride(t *testing.T) {
	cfg := fixtureConfig(t)
	spec, err := SpecForRole(cfg, config.RoleSpecInput{
		Name:          "integrator",
		ToolsOverride: &config.RoleToolsOverride{NoTools: true},
	}, SandboxConfig{})
	if err != nil {
		t.Fatalf("SpecForRole: %v", err)
	}
	args := SpecArgs(spec)
	if flagValue(args, "--tools") != "" {
		t.Errorf("--tools = %q, want empty (no tools)", flagValue(args, "--tools"))
	}
	found := false
	for _, a := range args {
		if a == "--strict-mcp-config" {
			found = true
		}
	}
	if !found {
		t.Error("expected --strict-mcp-config with NoTools override")
	}
	if spec.ExpectsReport || spec.InputPlane || spec.PreTimeoutNudge != "" {
		t.Errorf("utility-class role must not expect a report, run interactively, or get a nudge: %+v", spec)
	}
	if len(spec.Inline) != 0 {
		t.Errorf("utility-class role must never get the bridge, got %+v", spec.Inline)
	}
}

func TestSpecForRole_UtilityWithoutOverrideUsesRoleTools(t *testing.T) {
	cfg := fixtureConfig(t)
	cfg.Integrator = config.IntegratorConfig{BaseAgentConfig: config.BaseAgentConfig{
		Model:           "m",
		PermissionMode:  "default",
		AllowedTools:    []string{"Read", "Edit"},
		DisallowedTools: []string{"Bash", "Write"},
	}}
	spec, err := SpecForRole(cfg, config.RoleSpecInput{Name: "integrator"}, SandboxConfig{})
	if err != nil {
		t.Fatalf("SpecForRole: %v", err)
	}
	args := SpecArgs(spec)
	if flagValue(args, "--allowedTools") != "Read,Edit" {
		t.Errorf("--allowedTools = %q, want Read,Edit", flagValue(args, "--allowedTools"))
	}
	if flagValue(args, "--disallowedTools") != "Bash,Write" {
		t.Errorf("--disallowedTools = %q, want Bash,Write", flagValue(args, "--disallowedTools"))
	}
	// No forced additions for utility-class roles (unlike reporter).
	if strings.Contains(flagValue(args, "--allowedTools"), "mcp__") {
		t.Errorf("utility-class role must not get forced mcp__* additions, got %q", flagValue(args, "--allowedTools"))
	}
}
