package harness

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/xiii/orqestra/internal/config"
)

// SpecForRole builds the complete ProcessSpec for one config.Roles() table
// entry (WP14/RC4) — args, model routing, sandbox, and every orchestration
// knob (AgentID/ExpectsReport/PlanMode/InputPlane/Timeout/LoopGuard/
// SilenceGuard/PreTimeoutNudge) assembled in one place. It replaces the
// old per-process-config type and its per-role option functions (both
// deleted), and buildEngine's former ~200 lines of per-role assembly:
// adding a role is one YAML block (a config.RoleConfig, via roles: or a new
// built-in key wired into config.Config.Roles()) plus one call to
// SpecForRole from a literal pipeline call site — no new Go plumbing.
//
// sandbox is supplied by the caller (buildEngine builds the four sandbox
// tiers once at startup and passes the right one per invocation);
// SpecForRole does not construct sandboxes itself, only validates that the
// role names a recognized tier (config.RoleConfig.SandboxTier, checked by
// config.Config.Roles()).
func SpecForRole(cfg *config.Config, in config.RoleSpecInput, sandbox SandboxConfig) (ProcessSpec, error) {
	if in.Name == "" {
		return ProcessSpec{}, fmt.Errorf("spec for role: missing role name")
	}
	roles, err := cfg.Roles()
	if err != nil {
		return ProcessSpec{}, fmt.Errorf("spec for role %q: %w", in.Name, err)
	}
	role, ok := roles[in.Name]
	if !ok {
		return ProcessSpec{}, fmt.Errorf("spec for role %q: unknown role", in.Name)
	}

	resolved, err := cfg.ResolveModel(role.Model)
	if err != nil {
		return ProcessSpec{}, fmt.Errorf("spec for role %q: resolve model_ref %q: %w", in.Name, role.Model, err)
	}
	runtimeOpts, err := cfg.RuntimeOptions(role.Model)
	if err != nil {
		return ProcessSpec{}, fmt.Errorf("spec for role %q: runtime options for model_ref %q: %w", in.Name, role.Model, err)
	}

	model := ModelSpec{
		Provider: resolved.Type,
		Model:    resolved.Model,
		BaseURL:  resolved.BaseURL,
		APIKey:   resolved.APIKey,
	}
	if utility := cfg.ResolveUtilityModel(); utility != nil {
		model.SmallModel = utility.Model
	}

	binary := runtimeOpts.Binary
	if binary == "" {
		binary = "claude"
	}

	agentID := in.AgentID
	if agentID == "" {
		agentID = in.Name
	}

	var extraArgs []string
	var inline []InlineMCP
	var agents []InlineAgent

	switch role.Class {
	case config.RoleClassReporter:
		extraArgs = reporterArgs(role)
		if in.BridgeBinary != "" {
			inline = append(inline, bridgeMCP(in, agentID))
		}
		agents = append(agents, researcherAgent(cfg))
	case config.RoleClassExecutor:
		extraArgs = executorArgs(role)
		if in.BridgeBinary != "" {
			inline = append(inline, bridgeMCP(in, agentID))
		}
	case config.RoleClassUtility:
		extraArgs = utilityArgs(role, in)
	default:
		// Unreachable: cfg.Roles() already validated Class against
		// validRoleClasses. Fail closed anyway rather than silently build a
		// spec with no tool configuration at all.
		return ProcessSpec{}, fmt.Errorf("spec for role %q: unknown class %q", in.Name, role.Class)
	}

	if role.MaxTurns > 0 {
		extraArgs = append(extraArgs, "--max-turns", strconv.Itoa(role.MaxTurns))
	}

	spec := ProcessSpec{
		Model:        model,
		SystemPrompt: mergeAppendPrompts(role.SystemPrompt, role.AppendSystemPrompt),
		WorkDir:      in.WorkDir,
		Binary:       binary,
		ExtraArgs:    extraArgs,
		Inline:       inline,
		Agents:       agents,
		Sandbox:      sandbox,
		AgentID:      agentID,
		Timeout:      role.Timeout.Duration,
	}

	// Orchestration knobs that are a property of the role CLASS, not the
	// individual invocation (WP13/J6): reporter/executor roles run
	// interactively and expect a SubmitReport; utility roles are one-shot
	// and never harvest a report through ReportHarvester (see
	// orchestrator.RoleClassUtility's doc comment — the integrator's
	// one-shot invocations read Output/INTEGRATOR-GIVE-UP directly).
	if role.Class != config.RoleClassUtility {
		spec.ExpectsReport = true
		spec.InputPlane = true
		spec.PlanMode = role.PermissionMode == "plan"
		spec.LoopGuard = LoopGuardSpec{
			RepeatThreshold: role.LoopGuard.RepeatThreshold,
			MaxNudges:       role.LoopGuard.MaxNudges,
			CooldownTurns:   role.LoopGuard.CooldownTurns,
		}
		spec.SilenceGuard = SilenceGuardSpec{
			SilenceSecs: role.SilenceGuard.SilenceSecs,
			NudgeText:   role.SilenceGuard.NudgeText,
			MaxNudges:   role.SilenceGuard.MaxNudges,
		}
		nudge := role.PreTimeoutNudge
		if nudge == "" {
			nudge = config.DefaultPreTimeoutNudge(role.Class)
		}
		spec.PreTimeoutNudge = nudge
	}

	return spec, nil
}

// reporterArgs builds the tool/permission arguments for a RoleClassReporter
// role (architect/critic-shaped): permission-mode, then MCP-server
// filtering, then --allowedTools/--disallowedTools (both force-include the
// AskUserQuestion bridge routing), then the shared skip-permissions+settings
// pair every non-utility role gets so headless pipe-mode execution never
// blocks on an interactive prompt.
func reporterArgs(role config.RoleConfig) []string {
	disallowed := append([]string(nil), role.DisallowedTools...)
	if !containsString(disallowed, "AskUserQuestion") {
		disallowed = append(disallowed, "AskUserQuestion")
	}
	allowed := append([]string(nil), role.AllowedTools...)
	for _, tool := range []string{"mcp__*", "mcp__orqestra__AskUserQuestion"} {
		if !containsString(allowed, tool) {
			allowed = append(allowed, tool)
		}
	}

	var args []string
	if role.PermissionMode != "" {
		args = append(args, "--permission-mode", role.PermissionMode)
	}
	if role.MCPServers != nil {
		if len(*role.MCPServers) == 0 {
			args = append(args, noToolsArgs()...)
		} else {
			args = append(args, "--strict-mcp-config", "--mcp-config", filterMCPConfig(*role.MCPServers))
		}
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	if len(disallowed) > 0 {
		args = append(args, "--disallowedTools", strings.Join(disallowed, ","))
	}
	args = append(args, openAgentArgs()...)
	return args
}

// executorArgs builds the arguments for a RoleClassExecutor role
// (worker-shaped): only permission-mode plus the shared skip-permissions+
// settings pair — no tool filtering. Matches the pre-WP14 worker spec, which
// never routed through the reporter tool-filtering path (it never asks
// questions and needs the full default tool surface to execute a plan).
func executorArgs(role config.RoleConfig) []string {
	var args []string
	if role.PermissionMode != "" {
		args = append(args, "--permission-mode", role.PermissionMode)
	}
	args = append(args, openAgentArgs()...)
	return args
}

// utilityArgs builds the arguments for a RoleClassUtility role
// (integrator-shaped): a one-shot invocation with no bridge and no forced
// tool additions. in.ToolsOverride lets one role entry serve two invocation
// shapes (e.g. the integrator's commit-message pass disables every tool;
// its conflict-resolution pass grants exactly Read/Edit) — nil uses the
// role's own PermissionMode/AllowedTools/DisallowedTools verbatim.
func utilityArgs(role config.RoleConfig, in config.RoleSpecInput) []string {
	perm, allowed, disallowed := role.PermissionMode, role.AllowedTools, role.DisallowedTools
	noTools := false
	if ov := in.ToolsOverride; ov != nil {
		noTools = ov.NoTools
		perm, allowed, disallowed = ov.PermissionMode, ov.AllowedTools, ov.DisallowedTools
	}
	if noTools {
		return noToolsArgs()
	}
	var args []string
	if perm != "" {
		args = append(args, "--permission-mode", perm)
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	if len(disallowed) > 0 {
		args = append(args, "--disallowedTools", strings.Join(disallowed, ","))
	}
	return args
}

// noToolsArgs disables every built-in and MCP tool, making the CLI a pure
// text-in/text-out runner (dramatically reduces input token count and
// prevents the model from entering agentic tool-use mode).
func noToolsArgs() []string {
	return []string{"--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
}

// openAgentArgs are applied to every headless non-utility agent: all MCP
// tools pre-approved and interactive prompts bypassed. Single chokepoint —
// add here, not per-role.
func openAgentArgs() []string {
	return []string{"--dangerously-skip-permissions", `--settings`, `{"permissions":{"allow":["mcp__*"]}}`}
}

// bridgeMCP returns the "orqestra" inline MCP server definition (the
// AskUserQuestion/SubmitReport bridge) for one invocation.
func bridgeMCP(in config.RoleSpecInput, agentID string) InlineMCP {
	return InlineMCP{
		Name:    "orqestra",
		Command: in.BridgeBinary,
		Args:    []string{"mcp-bridge", "--socket", in.BridgeSocketPath, "--agent-id", agentID},
	}
}

// researcherAgent builds the inline "orqestra-researcher" subagent
// definition from cfg.Researcher — attached to every RoleClassReporter role
// (WP14/RC4: "researcher subagent attached to reporter roles" is now a
// class-level default, not per-role wiring). Model is intentionally OMITTED
// -> "inherit": cfg.Researcher.Model is an orqestra alias the CLI cannot
// read, and models are env-routed, so the subagent inherits the parent's
// model. The subagent has no MCP — it returns its report as its final
// message (its prompt's COMPLETION clause says so).
func researcherAgent(cfg *config.Config) InlineAgent {
	return InlineAgent{
		Name: "orqestra-researcher",
		Def: AgentDef{
			Description:     cfg.Researcher.Description,
			Prompt:          cfg.Researcher.SystemPrompt,
			Tools:           cfg.Researcher.AllowedTools,
			DisallowedTools: cfg.Researcher.DisallowedTools,
		},
	}
}

// containsString reports whether s contains v.
func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// mergeAppendPrompts concatenates non-empty prompt fragments into a single
// --append-system-prompt value. All system prompt steering goes through
// layer 5 (append) to preserve Claude Code's default identity, tool
// guidance, CLAUDE.md rules, and safety instructions (layer 4).
func mergeAppendPrompts(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			nonEmpty = append(nonEmpty, s)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

// filterMCPConfig reads the user's ~/.claude.json MCP server definitions and
// returns a JSON string containing only the named servers. This is passed to
// --mcp-config so only selected servers start, keeping token overhead minimal.
func filterMCPConfig(names []string) string {
	type mcpConfig struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("cannot determine home dir for MCP config", "err", err)
		return `{"mcpServers":{}}`
	}

	data, err := os.ReadFile(home + "/.claude.json")
	if err != nil {
		slog.Debug("no ~/.claude.json found, using empty MCP config")
		return `{"mcpServers":{}}`
	}

	var cfg mcpConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse ~/.claude.json", "err", err)
		return `{"mcpServers":{}}`
	}

	filtered := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		if server, ok := cfg.MCPServers[name]; ok {
			filtered[name] = server
		} else {
			slog.Warn("MCP server not found in ~/.claude.json", "name", name)
		}
	}

	result := mcpConfig{MCPServers: filtered}
	out, err := json.Marshal(result)
	if err != nil {
		return `{"mcpServers":{}}`
	}
	return string(out)
}
