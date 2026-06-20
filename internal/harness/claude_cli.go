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

// TokenUsage captures token consumption from an LLM call.
type TokenUsage struct {
	Input  int64
	Output int64
}

// Total returns the sum of input and output tokens.
func (u TokenUsage) Total() int64 { return u.Input + u.Output }

// RunResult captures the output and token usage from a CLIRunner invocation.
type RunResult struct {
	Output       string
	Usage        TokenUsage // zero value if the harness did not report usage
	SessionID    string     // populated from stream-json result event when available
	PlanFilePath string     // plan file path captured from result event (may be empty)
	ExitCode     int        // non-zero when the subprocess exited with an error (supporting evidence)
	Stderr       string     // stderr output captured from the subprocess
}

// ClaudeCLI implements the claude CLI binary integration.
type ClaudeCLI struct {
	resolved           config.ResolvedModel
	small              *config.ResolvedModel // optional small/fast model
	extraArgs          []string
	binary             string                  // path to claude binary, defaults to "claude"
	inlineMCPServers   map[string]inlineMCPDef // MCP servers injected at runtime
	appendSystemPrompt string                  // text appended to default system prompt via --append-system-prompt
	workDir            string                  // working directory for subprocess; empty inherits process CWD
}

// inlineMCPDef defines an MCP server to inject into --mcp-config.
type inlineMCPDef struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// NewClaudeCLI creates a *ClaudeCLI backed by the claude CLI binary.
func NewClaudeCLI(resolved config.ResolvedModel, opts ...ClaudeCLIOption) *ClaudeCLI {
	c := &ClaudeCLI{
		resolved: resolved,
		binary:   "claude",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// NewClaudeCLIFromConfig creates a *ClaudeCLI from a model_ref.
// Returns an error if modelRef is empty or cannot be resolved.
// Model-level runtime options are applied before caller-supplied options.
func NewClaudeCLIFromConfig(cfg *config.Config, modelRef string, opts ...ClaudeCLIOption) (*ClaudeCLI, error) {
	if modelRef == "" {
		return nil, fmt.Errorf("missing model_ref")
	}
	resolved, err := cfg.ResolveModel(modelRef)
	if err != nil {
		return nil, fmt.Errorf("resolve model_ref %q: %w", modelRef, err)
	}
	mopts, err := modelOptions(cfg, modelRef)
	if err != nil {
		return nil, fmt.Errorf("resolve model options for %q: %w", modelRef, err)
	}
	return NewClaudeCLI(resolved, append(mopts, opts...)...), nil
}

// BuildProcessSpec constructs a ProcessSpec from the same config+options used
// to build ClaudeCLI runners. The spec is suitable for use with harness.Run.
func BuildProcessSpec(cfg *config.Config, modelRef string, sandbox SandboxConfig, opts ...ClaudeCLIOption) (ProcessSpec, error) {
	cli, err := NewClaudeCLIFromConfig(cfg, modelRef, opts...)
	if err != nil {
		return ProcessSpec{}, err
	}
	return cli.toSpec(sandbox), nil
}

// toSpec converts the ClaudeCLI's current configuration into a ProcessSpec.
func (c *ClaudeCLI) toSpec(sandbox SandboxConfig) ProcessSpec {
	ms := ModelSpec{
		Provider: c.resolved.Type,
		Model:    c.resolved.Model,
		BaseURL:  c.resolved.BaseURL,
		APIKey:   c.resolved.APIKey,
	}
	if c.small != nil {
		ms.SmallModel = c.small.Model
	}

	var inline []InlineMCP
	for name, def := range c.inlineMCPServers {
		inline = append(inline, InlineMCP{Name: name, Command: def.Command, Args: def.Args})
	}

	return ProcessSpec{
		Model:        ms,
		SystemPrompt: c.appendSystemPrompt,
		WorkDir:      c.workDir,
		Binary:       c.binary,
		ExtraArgs:    append([]string(nil), c.extraArgs...),
		Inline:       inline,
		Sandbox:      sandbox,
	}
}

func modelOptions(cfg *config.Config, modelRef string) ([]ClaudeCLIOption, error) {
	runtime, err := cfg.RuntimeOptions(modelRef)
	if err != nil {
		return nil, fmt.Errorf("runtime options: %w", err)
	}
	var opts []ClaudeCLIOption
	if runtime.Binary != "" {
		opts = append(opts, WithBinary(runtime.Binary))
	}
	if utility := cfg.ResolveUtilityModel(); utility != nil {
		opts = append(opts, WithSmallModel(*utility))
	}
	return opts, nil
}

// ClaudeCLIOption configures ClaudeCLI.
type ClaudeCLIOption func(*ClaudeCLI)

// WithSmallModel sets a small/fast model for cheap inference.
func WithSmallModel(small config.ResolvedModel) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.small = &small
	}
}

// WithExtraArgs appends extra CLI arguments.
func WithExtraArgs(args ...string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.extraArgs = append(c.extraArgs, args...)
	}
}

// WithNoTools disables all built-in and MCP tools, making the CLI a pure
// text-in/text-out runner. This dramatically reduces input token count and
// prevents the model from entering agentic tool-use mode.
func WithNoTools() ClaudeCLIOption {
	return WithExtraArgs("--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
}

// WithMCPServers restricts which MCP servers start. Reads the user's MCP config
// and passes only the named servers via --strict-mcp-config. Servers not in the
// list never start and their tool definitions never reach the model.
// An empty slice means no MCP servers (equivalent to WithNoTools for MCP).
func WithMCPServers(names []string) ClaudeCLIOption {
	mcpCfg := filterMCPConfig(names)
	return WithExtraArgs("--strict-mcp-config", "--mcp-config", mcpCfg)
}

// WithAllowedTools restricts the CLI to only the specified tools.
// Tool names support patterns (e.g. "mcp__context7__*", "Read", "Grep").
func WithAllowedTools(tools []string) ClaudeCLIOption {
	return WithExtraArgs("--allowedTools", strings.Join(tools, ","))
}

// WithDisallowedTools blocks the specified tools from the CLI.
// Tool names support patterns (e.g. "mcp__MCP_DOCKER__*", "Bash").
func WithDisallowedTools(tools []string) ClaudeCLIOption {
	return WithExtraArgs("--disallowedTools", strings.Join(tools, ","))
}

// WithAppendSystemPrompt adds text to the --append-system-prompt flag.
// This preserves Claude Code's default identity, tool guidance, CLAUDE.md
// rules, and safety instructions (layer 4) while adding extra rules (layer 5).
// Multiple sources (role system prompt + this option) are merged at invocation.
func WithAppendSystemPrompt(text string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.appendSystemPrompt = text
	}
}

// MergeAppendPrompts concatenates non-empty prompt fragments into a single
// --append-system-prompt value. All system prompt steering goes through
// layer 5 (append) to preserve CLAUDE.md and the default prompt (layer 4).
func MergeAppendPrompts(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			nonEmpty = append(nonEmpty, s)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

// WithPermissionMode sets the --permission-mode flag (e.g. "plan", "default").
func WithPermissionMode(mode string) ClaudeCLIOption {
	if mode == "" {
		return func(*ClaudeCLI) {}
	}
	return WithExtraArgs("--permission-mode", mode)
}

// WithSettings passes a JSON settings object to the CLI via --settings.
// Example: WithSettings(`{"plansDirectory":"/tmp/plans"}`)
func WithSettings(jsonStr string) ClaudeCLIOption {
	return WithExtraArgs("--settings", jsonStr)
}

// WithBinary overrides the claude binary path.
func WithBinary(path string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.binary = path
	}
}

// WithMaxTurns limits the number of agentic turns. Zero or negative means no limit.
func WithMaxTurns(limit int) ClaudeCLIOption {
	if limit <= 0 {
		return func(*ClaudeCLI) {}
	}
	return WithExtraArgs("--max-turns", strconv.Itoa(limit))
}

// WithInlineMCPServer injects an MCP server definition that will be merged
// into the --mcp-config JSON at CLI invocation time. This is used to inject
// the orqestra question bridge as an MCP tool available to the model.
// When no explicit WithMCPServers filtering is active, inline servers are
// additive — the user's default MCPs from ~/.claude.json remain available.
func WithInlineMCPServer(name, command string, args []string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		if c.inlineMCPServers == nil {
			c.inlineMCPServers = make(map[string]inlineMCPDef)
		}
		c.inlineMCPServers[name] = inlineMCPDef{Command: command, Args: args}
	}
}

// WithWorkDir sets the working directory for the claude subprocess.
// When empty (the zero value), the subprocess inherits the process CWD.
func WithWorkDir(dir string) ClaudeCLIOption {
	return func(c *ClaudeCLI) { c.workDir = dir }
}

// --- Environment and args ---

// BuildModelEnv returns the environment variables needed to route the claude binary
// to the given model. Used by sandbox runners that exec claude inside a container.
// Returns an error if the provider type is empty or unknown — no fallback to native.
func BuildModelEnv(resolved config.ResolvedModel, utility *config.ResolvedModel) ([]string, error) {
	switch resolved.Type {
	case config.ProviderTypeNative:
		// no override — use native Claude CLI with logged-in credentials
		return nil, nil
	case config.ProviderTypeAnthropic:
		env := []string{
			"ANTHROPIC_BASE_URL=" + resolved.BaseURL,
			"ANTHROPIC_MODEL=" + resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL=" + resolved.Model,
		}
		if utility != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+utility.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+utility.Model,
			)
		}
		return env, nil
	case config.ProviderTypeOpenAI:
		baseURL := strings.TrimRight(resolved.BaseURL, "/")
		env := []string{
			"ANTHROPIC_BASE_URL=" + baseURL,
			"ANTHROPIC_MODEL=" + resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL=" + resolved.Model,
		}
		if utility != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+utility.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+utility.Model,
			)
		}
		return env, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q for model %q (valid: %q, %q, %q)",
			resolved.Type, resolved.Model,
			config.ProviderTypeNative, config.ProviderTypeAnthropic, config.ProviderTypeOpenAI)
	}
}

// buildEnv constructs the environment variables for the claude subprocess.
func (c *ClaudeCLI) buildEnv() ([]string, error) {
	modelEnv, err := BuildModelEnv(c.resolved, c.small)
	if err != nil {
		return nil, fmt.Errorf("build model env: %w", err)
	}
	// Filter variables that would make the subprocess behave as a child session
	// of the invoking Claude Code process. CLAUDE_CODE_SESSION_ID and
	// CLAUDE_CODE_CHILD_SESSION cause claude 2.1+ to attempt parent-session IPC
	// instead of starting an independent session, resulting in a hang.
	// Also strip ANTHROPIC_API_KEY to prevent leakage or conflicts.
	blocked := []string{
		"ANTHROPIC_API_KEY=",
		"CLAUDE_CODE_SESSION_ID=",
		"CLAUDE_CODE_CHILD_SESSION=",
	}
	var clean []string
	for _, kv := range os.Environ() {
		filtered := false
		for _, prefix := range blocked {
			if strings.HasPrefix(kv, prefix) {
				filtered = true
				break
			}
		}
		if !filtered {
			clean = append(clean, kv)
		}
	}
	return append(clean, modelEnv...), nil
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

// BuildTestArgs applies the given options to a fresh ClaudeCLI and returns the
// resulting CLI arguments. Intended for tests in packages that compose
// ClaudeCLIOption slices and need to verify the resulting argument list.
func BuildTestArgs(opts ...ClaudeCLIOption) []string {
	c := &ClaudeCLI{binary: "claude"}
	for _, opt := range opts {
		opt(c)
	}
	return c.buildFinalArgs()
}

// buildFinalArgs returns extraArgs with inline MCP servers merged in.
// buildFinalArgs merges inlineMCPServers into existing --mcp-config if present.
// When --strict-mcp-config was already set (via WithMCPServers), inline servers
// are merged into the existing filtered config. When no strict filtering was
// requested, inline servers are added via --mcp-config only (additive — the
// user's default MCPs from ~/.claude.json remain available).
func (c *ClaudeCLI) buildFinalArgs() []string {
	if len(c.inlineMCPServers) == 0 {
		return c.extraArgs
	}

	// Deep copy extraArgs so we can modify
	args := make([]string, len(c.extraArgs))
	copy(args, c.extraArgs)

	// Find and merge --mcp-config
	type mcpConfig struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	var existing mcpConfig
	mcpIdx := -1
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			mcpIdx = i + 1
			if err := json.Unmarshal([]byte(args[mcpIdx]), &existing); err != nil {
				existing = mcpConfig{MCPServers: make(map[string]json.RawMessage)}
			}
			break
		}
	}

	if existing.MCPServers == nil {
		existing.MCPServers = make(map[string]json.RawMessage)
	}

	// Merge inline servers
	for name, def := range c.inlineMCPServers {
		data, err := json.Marshal(def)
		if err != nil {
			slog.Error("buildFinalArgs: marshal inline MCP server", "name", name, "err", err)
			continue
		}
		existing.MCPServers[name] = data
	}

	merged, err := json.Marshal(existing)
	if err != nil {
		slog.Error("buildFinalArgs: marshal merged MCP config", "err", err)
		return c.extraArgs
	}

	if mcpIdx >= 0 {
		// --mcp-config already exists (with --strict-mcp-config from WithMCPServers);
		// update the config in place, keeping strict filtering.
		args[mcpIdx] = string(merged)
	} else {
		// No explicit MCP filtering — add inline servers additively.
		// Do NOT add --strict-mcp-config: let user's default MCPs from
		// ~/.claude.json remain available alongside the inline servers.
		args = append(args, "--mcp-config", string(merged))
	}

	return args
}

