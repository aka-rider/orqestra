package config

import (
	"fmt"
	"sort"
)

// RoleClass classifies a role's default spec-building behavior (WP14/RC4):
// whether harness.SpecForRole attaches the "orqestra" MCP bridge + inline
// researcher subagent and runs interactively with a report-harvest tier
// chain (reporter/executor), or builds a one-shot invocation with no bridge,
// no report harvesting, and no loop/silence/pre-timeout policies (utility).
// orchestrator.RoleClass (report_harvest.go) is a type alias of this type —
// report-harvest tier selection and spec building share one vocabulary.
type RoleClass string

const (
	// RoleClassReporter is researcher/architect/critic-shaped: interactive
	// input plane, the MCP bridge + inline researcher subagent attached,
	// full tool filtering (AskUserQuestion routed through the bridge),
	// SubmitReport -> plan-file -> final-message tier order.
	RoleClassReporter RoleClass = "reporter"
	// RoleClassExecutor is worker-shaped: interactive input plane, the MCP
	// bridge attached (no researcher subagent, no tool filtering beyond
	// permission_mode), SubmitReport -> raw-output tier order.
	RoleClassExecutor RoleClass = "executor"
	// RoleClassUtility is integrator-shaped: a one-shot -p invocation, no
	// bridge, no report harvesting, no loop/silence/pre-timeout policies.
	RoleClassUtility RoleClass = "utility"
)

// validRoleClasses is the set SpecForRole/Roles() accept; anything else is a
// configuration error (fail-closed, CLAUDE.md SS1.6).
var validRoleClasses = map[RoleClass]struct{}{
	RoleClassReporter: {},
	RoleClassExecutor: {},
	RoleClassUtility:  {},
}

// SandboxTier names one of the sandbox configurations the caller (buildEngine)
// constructs once at startup. harness.SpecForRole does not build sandboxes
// itself — the caller passes the resolved harness.SandboxConfig directly —
// but every RoleConfig must name a recognized tier so a YAML typo fails
// closed instead of silently resolving to the wrong sandbox.
type SandboxTier string

const (
	SandboxTierReadOnly       SandboxTier = "ro"
	SandboxTierWorkerWritable SandboxTier = "worker-writable"
	SandboxTierWorktree       SandboxTier = "worktree"
	SandboxTierConflict       SandboxTier = "conflict"
)

var validSandboxTiers = map[SandboxTier]struct{}{
	SandboxTierReadOnly:       {},
	SandboxTierWorkerWritable: {},
	SandboxTierWorktree:       {},
	SandboxTierConflict:       {},
}

// RoleConfig is one entry of the unified role table (WP14/RC4): every
// pipeline role (architect/critic/worker/integrator, and future QA/PM
// roles) is described by one RoleConfig, and harness.SpecForRole turns it
// into a complete ProcessSpec. Adding a new role is one YAML block (a
// roles: entry — see Config.ExtraRoles) plus one call to SpecForRole from a
// literal pipeline call site; no new Go plumbing (see
// TestSpecForRole_NewRoleZeroGoChanges in internal/harness).
type RoleConfig struct {
	BaseAgentConfig `yaml:",inline"`

	// Class selects reporter/executor/utility defaults. Required.
	Class RoleClass `yaml:"class"`
	// SandboxTier names which pre-built sandbox configuration this role's
	// primary invocation runs under. Required.
	SandboxTier SandboxTier `yaml:"sandbox_tier"`
	// PreTimeoutNudge overrides the class default nudge text sent shortly
	// before an invocation's deadline. "" uses DefaultPreTimeoutNudge(Class).
	PreTimeoutNudge string `yaml:"pre_timeout_nudge"`
	// Attempts is informational: a literal pipeline step's retry count for
	// this role. Today only the architect/critic/worker-validation steps
	// retry, and they read cfg.Retry directly (the pipeline stays literal Go
	// control flow, not a generic retry loop, per
	// plan-simplify-architecture.md's WP14 rule 4) — this field exists so a
	// new role's table entry can carry its own attempts count once a
	// literal step is written to consume it.
	Attempts int `yaml:"attempts"`
}

// applyBaseAgentDefaults merges d into a single agent's BaseAgentConfig in
// place: defaults.disallowed_tools/append_system_prompt apply only when the
// agent left its own field unset (replacement semantics, not a merge), and
// LoopGuard/SilenceGuard.MaxNudges get their zero-value defaults. Shared by
// Config.applyDefaults() (config.go, the five built-in roles) and
// applyRoleDefaults below (ExtraRoles).
func applyBaseAgentDefaults(a *BaseAgentConfig, d DefaultsConfig) {
	if len(a.DisallowedTools) == 0 && len(d.DisallowedTools) > 0 {
		a.DisallowedTools = append([]string(nil), d.DisallowedTools...)
	}
	if a.AppendSystemPrompt == "" && d.AppendSystemPrompt != "" {
		a.AppendSystemPrompt = d.AppendSystemPrompt
	}
	if a.LoopGuard.RepeatThreshold == 0 {
		a.LoopGuard.RepeatThreshold = 3
	}
	if a.LoopGuard.MaxNudges == 0 {
		a.LoopGuard.MaxNudges = 3
	}
	if a.LoopGuard.CooldownTurns == 0 {
		a.LoopGuard.CooldownTurns = 2
	}
	if a.SilenceGuard.MaxNudges == 0 {
		a.SilenceGuard.MaxNudges = 3
	}
}

// applyRoleDefaults gives every ExtraRoles entry (WP14/RC4) the same
// defaulting treatment as the five built-in roles: a new role defined only
// via roles: still inherits defaults.disallowed_tools/append_system_prompt
// and the LoopGuard/SilenceGuard zero-value defaults.
func applyRoleDefaults(extra map[string]RoleConfig, d DefaultsConfig) {
	for name, r := range extra {
		applyBaseAgentDefaults(&r.BaseAgentConfig, d)
		extra[name] = r
	}
}

// validate checks the fields SpecForRole depends on to fail closed instead
// of building a broken or ambiguous ProcessSpec (CLAUDE.md SS1.6). name is
// the Roles() table key, used only for error context.
func (r RoleConfig) validate(name string) error {
	if r.Model == "" {
		return fmt.Errorf("role %q: missing mandatory model_ref", name)
	}
	if _, ok := validRoleClasses[r.Class]; !ok {
		return fmt.Errorf("role %q: unknown class %q (valid: %q, %q, %q)",
			name, r.Class, RoleClassReporter, RoleClassExecutor, RoleClassUtility)
	}
	if _, ok := validSandboxTiers[r.SandboxTier]; !ok {
		return fmt.Errorf("role %q: unknown sandbox_tier %q (valid: %q, %q, %q, %q)",
			name, r.SandboxTier, SandboxTierReadOnly, SandboxTierWorkerWritable, SandboxTierWorktree, SandboxTierConflict)
	}
	return nil
}

// Roles returns the unified role table (WP14/RC4). The built-in
// architect/critic/worker/integrator keys are ALWAYS populated from their
// existing top-level YAML blocks (c.Architect/.Critic/.Worker/.Integrator) —
// old configs keep parsing unchanged (CLAUDE.md SS1.6). The roles: map (see
// Config.ExtraRoles) may add entirely new roles; a roles: key that collides
// with a built-in name is a configuration error (ambiguous authority).
// Every entry — built-in and extra — is validated (fail-closed on unknown
// class/sandbox_tier/empty model_ref, plus a live model_ref resolution
// check) in sorted key order, so a config with more than one bad entry
// always reports the same one first (CLAUDE.md SS1.7 determinism).
func (c *Config) Roles() (map[string]RoleConfig, error) {
	table := map[string]RoleConfig{
		"architect": {
			BaseAgentConfig: c.Architect.BaseAgentConfig,
			Class:           RoleClassReporter,
			SandboxTier:     SandboxTierReadOnly,
		},
		"critic": {
			BaseAgentConfig: c.Critic.BaseAgentConfig,
			Class:           RoleClassReporter,
			SandboxTier:     SandboxTierReadOnly,
		},
		"worker": {
			BaseAgentConfig: c.Worker.BaseAgentConfig,
			Class:           RoleClassExecutor,
			SandboxTier:     SandboxTierWorkerWritable,
		},
		"integrator": {
			BaseAgentConfig: c.Integrator.BaseAgentConfig,
			Class:           RoleClassUtility,
			SandboxTier:     SandboxTierReadOnly,
		},
	}
	for name, extra := range c.ExtraRoles {
		if _, exists := table[name]; exists {
			return nil, fmt.Errorf("roles: %q collides with a built-in role name", name)
		}
		table[name] = extra
	}

	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		role := table[name]
		if err := role.validate(name); err != nil {
			return nil, err
		}
		if _, err := c.ResolveModel(role.Model); err != nil {
			return nil, fmt.Errorf("role %q: %w", name, err)
		}
	}
	return table, nil
}

// DefaultPreTimeoutNudge returns the built-in nudge text sent shortly before
// an invocation's deadline for a role class, used when a RoleConfig leaves
// PreTimeoutNudge empty. RoleClassUtility defaults to "" (disabled) —
// matches the pre-WP14 integrator, which never set one.
func DefaultPreTimeoutNudge(class RoleClass) string {
	switch class {
	case RoleClassReporter:
		return genericReporterNudge
	case RoleClassExecutor:
		return genericExecutorNudge
	default:
		return ""
	}
}

// genericReporterNudge/genericExecutorNudge preserve, verbatim, the exact
// pre-WP14 nudge text (architect/critic and worker respectively) as
// class-level defaults, so the WP14 golden spec test stays byte-identical.
const genericReporterNudge = "[Orchestrator] Session deadline approaching. " +
	"Stop what you are doing and submit your report now by calling " +
	"mcp__orqestra__SubmitReport with the full markdown in the \"report\" argument. " +
	"Partial output is acceptable."

const genericExecutorNudge = "[Orchestrator] Session deadline approaching. " +
	"If your Validation Report is complete, call mcp__orqestra__SubmitReport " +
	"with the full report markdown now. " +
	"Otherwise continue executing — your file changes are already saved."

// RoleSpecInput selects a Roles() table entry and supplies the per-invocation
// values that legitimately vary across call sites sharing one logical role
// (WP14/RC4) — e.g. the integrator's commit-message and conflict-resolution
// invocations reuse the "integrator" RoleConfig entry with different work
// dirs and tool grants.
type RoleSpecInput struct {
	// Name is the Roles() table key. Required — harness.SpecForRole fails
	// closed when Name does not resolve.
	Name string
	// AgentID labels this invocation for middleware/report correlation.
	// "" defaults to Name.
	AgentID string
	// WorkDir is the subprocess working directory. "" inherits the process cwd.
	WorkDir string
	// BridgeSocketPath/BridgeBinary wire the "orqestra" inline MCP bridge
	// server (AskUserQuestion/SubmitReport) into reporter/executor-class
	// roles; utility-class roles never receive a bridge regardless of these
	// fields. BridgeBinary == "" disables the bridge for this invocation —
	// the degradation used when the self-executable path could not be
	// determined (questions/reports unavailable, run continues; CLAUDE.md
	// SS5.3).
	BridgeSocketPath string
	BridgeBinary     string
	// ToolsOverride replaces the role's own PermissionMode/AllowedTools/
	// DisallowedTools for THIS invocation only. nil uses the RoleConfig's
	// own values verbatim (no forced additions — that forcing is a
	// reporter-class-only behavior; see harness.SpecForRole).
	ToolsOverride *RoleToolsOverride
}

// RoleToolsOverride replaces a role's tool configuration for one invocation.
// NoTools takes priority over the other fields when true.
type RoleToolsOverride struct {
	NoTools         bool
	PermissionMode  string
	AllowedTools    []string
	DisallowedTools []string
}
