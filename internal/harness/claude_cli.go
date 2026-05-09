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
	Output    string
	Usage     TokenUsage // zero value if the harness did not report usage
	SessionID string     // populated from stream-json result event when available
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
	resolved  config.ResolvedModel
	small     *config.ResolvedModel // optional small/fast model
	extraArgs []string
	binary    string // path to claude binary, defaults to "claude"
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

// WithBinary overrides the claude binary path.
func WithBinary(path string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.binary = path
	}
}

// RunPrint runs `claude --print -p <prompt> --system-prompt <systemPrompt> --output-format json`
// and returns the output.
func (c *ClaudeCLI) RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error) {
	args := []string{"--print", "-p", prompt, "--output-format", "json"}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	args = append(args, c.extraArgs...)

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
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	args = append(args, c.extraArgs...)

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

		switch event.Type {
		case "assistant":
			// Extract text from message content blocks
			if text := event.extractAssistantText(); text != "" {
				stdout.Write([]byte(text))
			}
		case "content_block_delta":
			if event.Delta.Text != "" {
				stdout.Write([]byte(event.Delta.Text))
			}
		case "content_block_start":
			if sink, ok := stdout.(ActivitySink); ok {
				if name, args := event.extractToolUse(); name != "" {
					sink.OnToolUse(name, ToolDetail(name, args))
				}
			}
		case "result":
			result = event.Result
			resultIsError = event.IsError
			if event.SessionID != "" {
				sessionID = event.SessionID
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

	return RunResult{Output: result, Usage: usage, SessionID: sessionID}, nil
}

// RunContinue resumes a previous Claude CLI session with a follow-up prompt.
// Used for worker self-validation: the worker validates its own work in the
// same session that performed the implementation.
func (c *ClaudeCLI) RunContinue(ctx context.Context, sessionID, prompt string, stdout io.Writer) (RunResult, error) {
	args := []string{"--resume", sessionID, "-p", prompt, "--output-format", "stream-json", "--verbose"}
	args = append(args, c.extraArgs...)

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
		switch event.Type {
		case "assistant":
			if text := event.extractAssistantText(); text != "" && stdout != nil {
				stdout.Write([]byte(text))
			}
		case "content_block_delta":
			if event.Delta.Text != "" && stdout != nil {
				stdout.Write([]byte(event.Delta.Text))
			}
		case "content_block_start":
			if sink, ok := stdout.(ActivitySink); ok {
				if name, args := event.extractToolUse(); name != "" {
					sink.OnToolUse(name, ToolDetail(name, args))
				}
			}
		case "result":
			result = event.Result
			resultIsError = event.IsError
			if event.SessionID != "" {
				newSessionID = event.SessionID
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

	return RunResult{Output: result, Usage: usage, SessionID: newSessionID}, nil
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
