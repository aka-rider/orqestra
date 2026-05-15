package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/xiii/orqestra/internal/config"
)

// TokenUsage captures token consumption from an LLM call.
type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// RunResult captures the output and token usage from a CLIRunner invocation.
type RunResult struct {
	Output       string
	Usage        TokenUsage // zero value if the harness did not report usage
	SessionID    string     // populated from stream-json result event when available
	PlanFilePath string     // plan file path captured from result event (may be empty)
}

// CLIRunner is the interface for running claude CLI commands.
type CLIRunner interface {
	RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error)
	RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (RunResult, error)
}

// ContinuableRunner extends CLIRunner with session continuation support.
// Workers use this to self-validate in the same session.
type ContinuableRunner interface {
	CLIRunner
	// RunContinue resumes a previous session with a new prompt.
	// The session retains its tool state, conversation history, and sandbox.
	RunContinue(ctx context.Context, sessionID, prompt string, stdout io.Writer) (RunResult, error)
}

// ClaudeCLI executes the `claude` binary as a subprocess.
type ClaudeCLI struct {
	resolved         config.ResolvedModel
	small            *config.ResolvedModel // optional small/fast model
	extraArgs        []string
	binary           string                  // path to claude binary, defaults to "claude"
	inlineMCPServers map[string]inlineMCPDef // MCP servers injected at runtime
}

// inlineMCPDef defines an MCP server to inject into --mcp-config.
type inlineMCPDef struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// NewClaudeCLI creates a CLIRunner backed by the claude CLI binary.
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

// NewClaudeCLIFromConfig creates a Claude CLI runner from a model_ref.
// Returns an error if modelRef is empty or cannot be resolved.
// Model-level runtime options are applied before caller-supplied options.
func NewClaudeCLIFromConfig(cfg *config.Config, modelRef string, opts ...ClaudeCLIOption) (CLIRunner, error) {
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
	return WithExtraArgs("--allowed-tools", strings.Join(tools, ","))
}

// WithDisallowedTools blocks the specified tools from the CLI.
// Tool names support patterns (e.g. "mcp__MCP_DOCKER__*", "Bash").
func WithDisallowedTools(tools []string) ClaudeCLIOption {
	return WithExtraArgs("--disallowed-tools", strings.Join(tools, ","))
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

// RunPrint runs `claude --print -p <prompt> --system-prompt <systemPrompt> --output-format json`
// and returns the output.
func (c *ClaudeCLI) RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error) {
	args := []string{"--print", "-p", prompt, "--output-format", "json"}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	args = append(args, c.buildFinalArgs()...)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Env = c.buildEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return RunResult{}, fmt.Errorf("claude CLI error: %w (stderr: %s)", err, stderr.String())
	}

	raw := stdout.String()

	// Extract token usage from JSON envelope if present.
	var envelope struct {
		Usage *streamUsage `json:"usage"`
	}
	var usage TokenUsage
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Usage != nil {
		usage = TokenUsage{
			InputTokens:  envelope.Usage.InputTokens,
			OutputTokens: envelope.Usage.OutputTokens,
			TotalTokens:  envelope.Usage.InputTokens + envelope.Usage.OutputTokens,
		}
	}

	return RunResult{Output: raw, Usage: usage}, nil
}

// RunStreaming runs `claude -p <prompt> --system-prompt <systemPrompt> --output-format stream-json --verbose`
// and streams displayable content to stdout. Returns the final result text.
func (c *ClaudeCLI) RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (RunResult, error) {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	args = append(args, c.buildFinalArgs()...)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Env = c.buildEnv()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("claude CLI stdout pipe error: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("claude CLI start error: %w", err)
	}

	var result string
	var resultIsError bool
	var usage TokenUsage
	var sessionID string
	var planFilePath string
	scanner := bufio.NewScanner(cmdStdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large lines
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Not valid JSON — write raw to stdout and log for diagnostics
			slog.Debug("non-JSON stream line from claude CLI", "err", err, "line_len", len(line))
			stdout.Write(line)
			stdout.Write([]byte("\n"))
			continue
		}

		// Capture session ID from any event that includes it
		if event.SessionID != "" {
			sessionID = event.SessionID
		}

		dispatchStreamEvent(event, stdout)
		switch event.Type {
		case "result":
			result = event.Result
			resultIsError = event.IsError
			if event.SessionID != "" {
				sessionID = event.SessionID
			}
			if event.PlanFilePath != "" {
				planFilePath = event.PlanFilePath
			}
			if event.Usage != nil {
				usage = TokenUsage{
					InputTokens:  event.Usage.InputTokens,
					OutputTokens: event.Usage.OutputTokens,
					TotalTokens:  event.Usage.InputTokens + event.Usage.OutputTokens,
				}
			}
		}
	}

	cmdErr := cmd.Wait()
	if cmdErr != nil {
		// If we got a result with is_error:true, surface that as the error message
		if resultIsError && result != "" {
			return RunResult{}, fmt.Errorf("claude CLI error: %s", result)
		}
		return RunResult{}, fmt.Errorf("claude CLI error: %w (stderr: %s)", cmdErr, stderr.String())
	}

	if resultIsError {
		return RunResult{}, fmt.Errorf("claude CLI error: %s", result)
	}

	if result == "" {
		return RunResult{}, fmt.Errorf("claude CLI produced no result message in stream")
	}

	return RunResult{Output: result, Usage: usage, SessionID: sessionID, PlanFilePath: planFilePath}, nil
}

// RunContinue resumes a previous Claude CLI session with a follow-up prompt.
// Used for worker self-validation: the worker validates its own work in the
// same session that performed the implementation.
func (c *ClaudeCLI) RunContinue(ctx context.Context, sessionID, prompt string, stdout io.Writer) (RunResult, error) {
	args := []string{"--resume", sessionID, "-p", prompt, "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	args = append(args, c.buildFinalArgs()...)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Env = c.buildEnv()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("claude CLI stdout pipe error: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("claude CLI start error: %w", err)
	}

	var result string
	var resultIsError bool
	var usage TokenUsage
	var newSessionID string
	var planFilePath string
	scanner := bufio.NewScanner(cmdStdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			if stdout != nil {
				stdout.Write(line)
				stdout.Write([]byte("\n"))
			}
			continue
		}
		if event.SessionID != "" {
			newSessionID = event.SessionID
		}
		dispatchStreamEvent(event, stdout)
		switch event.Type {
		case "result":
			result = event.Result
			resultIsError = event.IsError
			if event.SessionID != "" {
				newSessionID = event.SessionID
			}
			if event.PlanFilePath != "" {
				planFilePath = event.PlanFilePath
			}
			if event.Usage != nil {
				usage = TokenUsage{
					InputTokens:  event.Usage.InputTokens,
					OutputTokens: event.Usage.OutputTokens,
					TotalTokens:  event.Usage.InputTokens + event.Usage.OutputTokens,
				}
			}
		}
	}

	cmdErr := cmd.Wait()
	if cmdErr != nil {
		if resultIsError && result != "" {
			return RunResult{}, fmt.Errorf("claude CLI continue error: %s", result)
		}
		return RunResult{}, fmt.Errorf("claude CLI continue error: %w (stderr: %s)", cmdErr, stderr.String())
	}

	if resultIsError {
		return RunResult{}, fmt.Errorf("claude CLI continue error: %s", result)
	}

	return RunResult{Output: result, Usage: usage, SessionID: newSessionID, PlanFilePath: planFilePath}, nil
}

// streamEvent represents a parsed event from Claude CLI's stream-json output.
type streamEvent struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype,omitempty"`
	Result       string          `json:"result,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Delta        streamDeltaText `json:"delta,omitempty"`
	Message      json.RawMessage `json:"message,omitempty"`
	ContentBlock json.RawMessage `json:"content_block,omitempty"`
	Usage        *streamUsage    `json:"usage,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	PlanFilePath string          `json:"planFilePath,omitempty"` // plan file path from result event
	Event        json.RawMessage `json:"event,omitempty"`        // inner event for stream_event wrapper
}

// streamUsage captures token usage from the Claude CLI result event.
type streamUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type streamDeltaText struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

// extractAssistantText pulls text content from an assistant message event.
func (e *streamEvent) extractAssistantText() string {
	if e.Message == nil {
		return ""
	}
	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return ""
	}
	var b bytes.Buffer
	for _, block := range msg.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

type toolUseBlock struct {
	Name  string
	Input json.RawMessage
}

func (e *streamEvent) extractAssistantToolUses() []toolUseBlock {
	if e.Message == nil {
		return nil
	}
	var msg struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return nil
	}
	var tools []toolUseBlock
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			tools = append(tools, toolUseBlock{Name: block.Name, Input: block.Input})
		}
	}
	return tools
}

// extractToolUse extracts the tool name and input args from a content_block_start event.
// Claude CLI emits: {"type":"content_block_start","content_block":{"type":"tool_use","name":"Read","input":{...}}}
func (e *streamEvent) extractToolUse() (name string, args json.RawMessage) {
	if e.ContentBlock == nil {
		return "", nil
	}
	var block struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(e.ContentBlock, &block); err == nil && block.Type == "tool_use" {
		return block.Name, block.Input
	}
	return "", nil
}

// dispatchStreamEvent writes the human-readable content of a single stream-json
// event to display, and fires OnToolUse on display if it implements ActivitySink.
// Used by both ClaudeCLI and SandboxCLIRunner to avoid duplicating the switch logic.
// display may be nil (e.g. orchestrator commit-message RunContinue(..., nil)).
func dispatchStreamEvent(event streamEvent, display io.Writer) {
	if display == nil {
		return
	}
	switch event.Type {
	case "assistant":
		if text := event.extractAssistantText(); text != "" {
			display.Write([]byte(text)) // nolint:errcheck — best-effort stream display
		}
		if sink, ok := display.(ActivitySink); ok {
			for _, tu := range event.extractAssistantToolUses() {
				sink.OnToolUse(tu.Name, ToolDetail(tu.Name, tu.Input))
			}
		}
	case "content_block_delta":
		if event.Delta.Text != "" {
			display.Write([]byte(event.Delta.Text)) // nolint:errcheck
		}
	case "content_block_start":
		if sink, ok := display.(ActivitySink); ok {
			if name, args := event.extractToolUse(); name != "" {
				sink.OnToolUse(name, ToolDetail(name, args))
			}
		}
	case "stream_event":
		if event.Event == nil {
			return
		}
		var inner streamEvent
		if err := json.Unmarshal(event.Event, &inner); err != nil {
			return
		}
		switch inner.Type {
		case "content_block_start":
			if sink, ok := display.(ActivitySink); ok {
				if name, args := inner.extractToolUse(); name != "" {
					sink.OnToolUse(name, ToolDetail(name, args))
				}
			}
		case "content_block_delta":
			if inner.Delta.Text != "" {
				display.Write([]byte(inner.Delta.Text)) // nolint:errcheck
			}
		}
	}
}

// BuildModelEnv returns the environment variables needed to route the claude binary
// to the given model. Used by sandbox runners that exec claude inside a container.
func BuildModelEnv(resolved config.ResolvedModel, utility *config.ResolvedModel) []string {
	var env []string
	switch resolved.Type {
	case "native":
		// no override
	case "anthropic":
		env = append(env,
			"ANTHROPIC_BASE_URL="+resolved.BaseURL,
			"ANTHROPIC_MODEL="+resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL="+resolved.Model,
		)
		if utility != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+utility.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+utility.Model,
			)
		}
	case "openai":
		baseURL := strings.TrimRight(resolved.BaseURL, "/")
		env = append(env,
			"ANTHROPIC_BASE_URL="+baseURL,
			"ANTHROPIC_MODEL="+resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL="+resolved.Model,
		)
		if utility != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+utility.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+utility.Model,
			)
		}
	}
	return env
}

// buildEnv constructs the environment variables for the claude subprocess.
func (c *ClaudeCLI) buildEnv() []string {
	env := os.Environ()
	env = append(env, BuildModelEnv(c.resolved, c.small)...)
	return env
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
