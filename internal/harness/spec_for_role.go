package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
		extraArgs, err = reporterArgs(role)
		if err != nil {
			return ProcessSpec{}, fmt.Errorf("spec for role %q: %w", in.Name, err)
		}
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
func reporterArgs(role config.RoleConfig) ([]string, error) {
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
			filtered, err := filterMCPConfig(*role.MCPServers)
			if err != nil {
				return nil, fmt.Errorf("reporter args: %w", err)
			}
			args = append(args, "--strict-mcp-config", "--mcp-config", filtered)
		}
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	if len(disallowed) > 0 {
		args = append(args, "--disallowedTools", strings.Join(disallowed, ","))
	}
	args = append(args, openAgentArgs()...)
	return args, nil
}

// executorArgs builds the arguments for a RoleClassExecutor role
// (worker-shaped): permission-mode plus the shared skip-permissions+settings
// pair, matching the pre-WP14 worker spec exactly — UNLESS the role's config
// explicitly sets AllowedTools, in which case §1.6 requires that explicit
// intent take effect (a config gate reviewers pinned by name must actually
// gate something).
//
// The gate is role.AllowedTools, not role.DisallowedTools: DefaultsConfig's
// blanket "defaults.disallowed_tools" (pipeline.yaml) backfills EVERY role's
// DisallowedTools — including the worker's — whenever the role itself leaves
// it unset (config/roles.go's applyBaseAgentDefaults), so DisallowedTools is
// essentially NEVER empty post-Load() even for a worker role that configured
// nothing. AllowedTools has no such blanket-default field, so a non-empty
// AllowedTools is an unambiguous signal that THIS role explicitly configured
// its tool surface — at that point role.DisallowedTools (explicit or
// defaulted) is included too, mirroring reporterArgs' pairing of the two.
// A worker that sets ONLY disallowed_tools (no allowed_tools) is a known gap
// left by this signal — flagged for a future pass; today's args (matching
// pre-WP14 behavior) are preserved whenever AllowedTools is unset, keeping
// every existing config (including cmd/orqestra/testdata/wp14_golden.yaml,
// which sets neither) byte-identical.
func executorArgs(role config.RoleConfig) []string {
	var args []string
	if role.PermissionMode != "" {
		args = append(args, "--permission-mode", role.PermissionMode)
	}
	if len(role.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(role.AllowedTools, ","))
		if len(role.DisallowedTools) > 0 {
			args = append(args, "--disallowedTools", strings.Join(role.DisallowedTools, ","))
		}
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
//
// A6/§1.6: names arriving here are an explicit, user-NAMED config intent
// (role.MCPServers in orqestra.yaml) — a name that is not found (whether
// because ~/.claude.json itself is missing/unreadable/corrupt, or the file
// exists but lacks that entry) is a config error, not something to warn about
// and silently drop: the caller asked for a specific server and would
// otherwise run with tools missing and no indication why. Fails closed
// instead (was: warn + continue with an empty/partial server set).
func filterMCPConfig(names []string) (string, error) {
	type mcpConfig struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("filter mcp config: determine home dir: %w", err)
	}

	claudeJSONPath := home + "/.claude.json"
	var cfg mcpConfig
	data, err := os.ReadFile(claudeJSONPath)
	switch {
	case err == nil:
		if unmarshalErr := json.Unmarshal(data, &cfg); unmarshalErr != nil {
			return "", fmt.Errorf("filter mcp config: parse %s: %w", claudeJSONPath, unmarshalErr)
		}
	case os.IsNotExist(err):
		// No file at all: cfg.MCPServers stays nil, so every named server
		// below is reported missing — the same fail-closed outcome as an
		// existing file lacking those entries, not a silent empty config.
	default:
		return "", fmt.Errorf("filter mcp config: read %s: %w", claudeJSONPath, err)
	}

	filtered := make(map[string]json.RawMessage, len(names))
	var missing []string
	for _, name := range names {
		if server, ok := cfg.MCPServers[name]; ok {
			filtered[name] = server
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		sort.Strings(missing) // §1.7: deterministic error message regardless of config iteration order
		return "", fmt.Errorf("filter mcp config: named MCP server(s) not found in %s: %s", claudeJSONPath, strings.Join(missing, ", "))
	}

	result := mcpConfig{MCPServers: filtered}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("filter mcp config: marshal filtered config: %w", err)
	}
	return string(out), nil
}
