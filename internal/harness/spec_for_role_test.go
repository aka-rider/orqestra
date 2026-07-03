package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
)

// withClaudeJSONFixture points HOME at an isolated temp dir and writes a
// ~/.claude.json containing an entry for each name in servers, so tests that
// exercise filterMCPConfig's "found" path (A6: SpecForRole now fails closed
// on a named-but-missing server) never depend on the developer/CI machine's
// real ~/.claude.json.
func withClaudeJSONFixture(t *testing.T, servers ...string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	entries := make(map[string]any, len(servers))
	for _, name := range servers {
		entries[name] = map[string]any{"command": "true"}
	}
	data, err := json.Marshal(map[string]any{"mcpServers": entries})
	if err != nil {
		t.Fatalf("marshal claude.json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), data, 0o644); err != nil {
		t.Fatalf("write claude.json fixture: %v", err)
	}
}

// fixtureConfig returns a minimal, valid *config.Config: one provider, one
// model ("m"), and architect/critic/worker/integrator all pointed at it.
// Individual tests overwrite whichever role/field they exercise.
func fixtureConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {BaseURL: "http://localhost:9999", Type: config.ProviderTypeOpenAI},
		},
		Models: map[string]config.ModelConfig{
			"m": {Provider: "local", Model: "test-model"},
		},
		Architect:  config.ArchitectConfig{BaseAgentConfig: config.BaseAgentConfig{Model: "m", PermissionMode: "plan"}},
		Critic:     config.CriticConfig{BaseAgentConfig: config.BaseAgentConfig{Model: "m"}},
		Worker:     config.WorkerConfig{BaseAgentConfig: config.BaseAgentConfig{Model: "m"}},
		Integrator: config.IntegratorConfig{BaseAgentConfig: config.BaseAgentConfig{Model: "m"}},
	}
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// --- Fail-closed gates (WP14 Step 3) ---

func TestSpecForRole_UnknownRoleName_Errors(t *testing.T) {
	// INV-P5-FAILCLOSED: an unknown role name must error, never build a zero spec.
	cfg := fixtureConfig(t)
	_, err := SpecForRole(cfg, config.RoleSpecInput{Name: "nonexistent"}, SandboxConfig{})
	if err == nil {
		t.Fatal("expected error for unknown role name, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error = %q, want it to contain the role name", err)
	}
}

func TestSpecForRole_UnknownClass_Errors(t *testing.T) {
	// INV-P5-FAILCLOSED: an unrecognized class must error, not silently build
	// a spec with no tool configuration.
	cfg := fixtureConfig(t)
	cfg.ExtraRoles = map[string]config.RoleConfig{
		"qa": {
			BaseAgentConfig: config.BaseAgentConfig{Model: "m"},
			Class:           "bogus-class",
			SandboxTier:     config.SandboxTierReadOnly,
		},
	}
	_, err := SpecForRole(cfg, config.RoleSpecInput{Name: "qa"}, SandboxConfig{})
	if err == nil {
		t.Fatal("expected error for unknown class, got nil")
	}
	if !strings.Contains(err.Error(), "class") {
		t.Errorf("error = %q, want it to mention class", err)
	}
}

func TestSpecForRole_MissingModelRef_Errors(t *testing.T) {
	// INV-P5-FAILCLOSED: empty model_ref is explicit misconfiguration, must error.
	cfg := fixtureConfig(t)
	cfg.ExtraRoles = map[string]config.RoleConfig{
		"qa": {
			BaseAgentConfig: config.BaseAgentConfig{Model: ""},
			Class:           config.RoleClassReporter,
			SandboxTier:     config.SandboxTierReadOnly,
		},
	}
	_, err := SpecForRole(cfg, config.RoleSpecInput{Name: "qa"}, SandboxConfig{})
	if err == nil {
		t.Fatal("expected error for missing model_ref, got nil")
	}
	if !strings.Contains(err.Error(), "model_ref") {
		t.Errorf("error = %q, want it to mention model_ref", err)
	}
}

func TestSpecForRole_UnknownSandboxTier_Errors(t *testing.T) {
	// INV-P5-FAILCLOSED: a sandbox_tier typo must error, not silently resolve
	// to the wrong sandbox.
	cfg := fixtureConfig(t)
	cfg.ExtraRoles = map[string]config.RoleConfig{
		"qa": {
			BaseAgentConfig: config.BaseAgentConfig{Model: "m"},
			Class:           config.RoleClassReporter,
			SandboxTier:     "bogus-tier",
		},
	}
	_, err := SpecForRole(cfg, config.RoleSpecInput{Name: "qa"}, SandboxConfig{})
	if err == nil {
		t.Fatal("expected error for unknown sandbox_tier, got nil")
	}
	if !strings.Contains(err.Error(), "sandbox_tier") {
		t.Errorf("error = %q, want it to mention sandbox_tier", err)
	}
}

// TestSpecForRole_NamedMCPServerMissing_Errors is WP18's A6 QA gate: a
// user-NAMED mcp_servers entry that does not exist in ~/.claude.json is
// explicit user intent that cannot be satisfied — SpecForRole must fail
// closed (§1.6), never warn-and-silently-drop the missing server as pre-A6
// filterMCPConfig did.
//
// RED-first: against the pre-A6 filterMCPConfig (slog.Warn + continue with
// whatever servers WERE found), this test failed because SpecForRole
// returned a valid spec with no error at all — the missing server vanished
// with only a Warn log to notice it.
func TestSpecForRole_NamedMCPServerMissing_Errors(t *testing.T) {
	withClaudeJSONFixture(t, "present-server") // "missing-server" is deliberately absent

	cfg := fixtureConfig(t)
	named := []string{"present-server", "missing-server"}
	cfg.Architect = config.ArchitectConfig{BaseAgentConfig: config.BaseAgentConfig{
		Model:          "m",
		PermissionMode: "plan",
		MCPServers:     &named,
	}}

	_, err := SpecForRole(cfg, config.RoleSpecInput{Name: "architect"}, SandboxConfig{})
	if err == nil {
		t.Fatal("expected an error for a named-but-missing MCP server, got nil")
	}
	if !strings.Contains(err.Error(), "missing-server") {
		t.Errorf("error = %q, want it to name the missing server %q", err, "missing-server")
	}
}

func TestSpecForRole_UnresolvableModelRef_Errors(t *testing.T) {
	// INV-P5-FAILCLOSED: a model_ref that doesn't resolve to a configured
	// model must error (typo/missing model), distinct from an empty ref.
	cfg := fixtureConfig(t)
	cfg.ExtraRoles = map[string]config.RoleConfig{
		"qa": {
			BaseAgentConfig: config.BaseAgentConfig{Model: "does-not-exist"},
			Class:           config.RoleClassReporter,
			SandboxTier:     config.SandboxTierReadOnly,
		},
	}
	_, err := SpecForRole(cfg, config.RoleSpecInput{Name: "qa"}, SandboxConfig{})
	if err == nil {
		t.Fatal("expected error for unresolvable model_ref, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error = %q, want it to contain the bad model ref", err)
	}
}

// --- New-role gate (WP14 Step 3, second gate) ---

// TestSpecForRole_NewRoleZeroGoChanges proves adding a role is one YAML
// block (here, a roles: entry built in-memory) plus one SpecForRole call —
// zero Go changes. Every field a pipeline call site needs is populated.
func TestSpecForRole_NewRoleZeroGoChanges(t *testing.T) {
	cfg := fixtureConfig(t)
	cfg.Models["fast"] = config.ModelConfig{Provider: "local", Model: "test-fast"}
	cfg.ExtraRoles = map[string]config.RoleConfig{
		"qa": {
			BaseAgentConfig: config.BaseAgentConfig{
				Model:          "fast",
				PermissionMode: "plan",
				SystemPrompt:   "QA role system prompt",
				Timeout:        config.Duration{Duration: 5 * time.Minute},
				MaxTurns:       10,
			},
			Class:       config.RoleClassReporter,
			SandboxTier: config.SandboxTierReadOnly,
		},
	}

	spec, err := SpecForRole(cfg, config.RoleSpecInput{
		Name:             "qa",
		WorkDir:          "/fixture/repo",
		BridgeSocketPath: "/tmp/qa.sock",
		BridgeBinary:     "/usr/bin/true",
	}, SandboxConfig{RepoPath: "/fixture/repo"})
	if err != nil {
		t.Fatalf("SpecForRole returned error for a valid new role: %v", err)
	}

	if spec.AgentID != "qa" {
		t.Errorf("AgentID = %q, want %q", spec.AgentID, "qa")
	}
	if spec.Model.Model != "test-fast" {
		t.Errorf("Model.Model = %q, want %q", spec.Model.Model, "test-fast")
	}
	if spec.Binary == "" {
		t.Error("Binary must not be empty (defaults to claude)")
	}
	if spec.WorkDir != "/fixture/repo" {
		t.Errorf("WorkDir = %q, want /fixture/repo", spec.WorkDir)
	}
	if spec.Sandbox.RepoPath != "/fixture/repo" {
		t.Errorf("Sandbox.RepoPath = %q, want /fixture/repo", spec.Sandbox.RepoPath)
	}
	if !spec.ExpectsReport {
		t.Error("ExpectsReport should be true for a reporter-class role")
	}
	if !spec.InputPlane {
		t.Error("InputPlane should be true for a reporter-class role")
	}
	if !spec.PlanMode {
		t.Error("PlanMode should be true (permission_mode: plan)")
	}
	if spec.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v, want 5m", spec.Timeout)
	}
	if spec.PreTimeoutNudge == "" {
		t.Error("PreTimeoutNudge should default to the reporter class nudge")
	}
	if len(spec.Inline) != 1 || spec.Inline[0].Name != "orqestra" {
		t.Errorf("expected the orqestra bridge attached, got %+v", spec.Inline)
	}
	if len(spec.Agents) != 1 || spec.Agents[0].Name != "orqestra-researcher" {
		t.Errorf("expected the researcher subagent attached to a reporter role, got %+v", spec.Agents)
	}

	args, err := SpecArgs(spec)
	if err != nil {
		t.Fatalf("SpecArgs: %v", err)
	}
	if flagValue(args, "--max-turns") != "10" {
		t.Errorf("--max-turns = %q, want 10", flagValue(args, "--max-turns"))
	}
	if !strings.Contains(flagValue(args, "--allowedTools"), "mcp__orqestra__AskUserQuestion") {
		t.Errorf("--allowedTools = %q, want mcp__orqestra__AskUserQuestion forced in", flagValue(args, "--allowedTools"))
	}
	if !strings.Contains(flagValue(args, "--append-system-prompt"), "QA role system prompt") {
		t.Errorf("--append-system-prompt = %q, want the role's system prompt", flagValue(args, "--append-system-prompt"))
	}
}
