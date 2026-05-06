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
	Output string
	Usage  *TokenUsage // nil if the harness did not report usage
}

// CLIRunner is the interface for running claude CLI commands.
type CLIRunner interface {
	RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error)
	RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (RunResult, error)
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
	if small := cfg.ResolveSmallModel(); small != nil {
		opts = append(opts, WithSmallModel(*small))
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

	return RunResult{Output: stdout.String()}, nil
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
	var usage *TokenUsage
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
		case "result":
			result = event.Result
			if event.Usage != nil {
				usage = &TokenUsage{
					InputTokens:  event.Usage.InputTokens,
					OutputTokens: event.Usage.OutputTokens,
					TotalTokens:  event.Usage.InputTokens + event.Usage.OutputTokens,
				}
			}
			// system, tool_use, tool_result, etc. — skip for display
		}
	}

	cmdErr := cmd.Wait()
	if cmdErr != nil {
		return RunResult{}, fmt.Errorf("claude CLI error: %w (stderr: %s)", cmdErr, stderr.String())
	}

	if result == "" {
		return RunResult{}, fmt.Errorf("claude CLI produced no result message in stream")
	}

	return RunResult{Output: result, Usage: usage}, nil
}

// streamEvent represents a parsed event from Claude CLI's stream-json output.
type streamEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Result  string          `json:"result,omitempty"`
	Delta   streamDeltaText `json:"delta,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
	Usage   *streamUsage    `json:"usage,omitempty"`
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

// BuildModelEnv returns the environment variables needed to route the claude binary
// to the given model. Used by sandbox runners that exec claude inside a container.
func BuildModelEnv(resolved config.ResolvedModel, small *config.ResolvedModel) []string {
	var env []string
	switch resolved.Type {
	case "native":
		// no override
	case "anthropic":
		env = append(env,
			"ANTHROPIC_BASE_URL="+resolved.BaseURL,
			"ANTHROPIC_AUTH_TOKEN="+resolved.APIKey,
			"ANTHROPIC_MODEL="+resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL="+resolved.Model,
		)
		if small != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+small.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+small.Model,
			)
		}
	case "openai":
		// Claude Code CLI uses the Anthropic API path natively. When targeting
		// an OpenAI-compatible server (Ollama, vLLM, etc.) that also speaks the
		// Anthropic messages format, route via ANTHROPIC_BASE_URL so the CLI
		// handles auth and streaming correctly.
		baseURL := strings.TrimRight(resolved.BaseURL, "/")
		env = append(env,
			"ANTHROPIC_BASE_URL="+baseURL,
			"ANTHROPIC_MODEL="+resolved.Model,
		)
		if resolved.APIKey != "" {
			env = append(env, "ANTHROPIC_API_KEY="+resolved.APIKey)
		}
		if small != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+small.Model,
			)
		}
	}
	env = append(env,
		"DISABLE_NON_ESSENTIAL_MODEL_CALLS=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
	return env
}

// BuildPTYCommand builds the Claude Code CLI launch command for PTY-mode execution.
// Non-interactive mode (intake/validation) uses -p with the prompt text.
// Interactive mode (worker) launches the full interactive CLI.
// Both modes skip permissions — the sandbox is already isolated.
// If systemPromptFile is non-empty, --append-system-prompt-file is added.
func BuildPTYCommand(prompt string, interactive bool) []string {
	if interactive {
		return []string{"claude", "--dangerously-skip-permissions"}
	}
	return []string{"claude", "--dangerously-skip-permissions", "-p", prompt, "--output-format", "stream-json", "--verbose"}
}

// BuildPTYCommandWithPromptFile builds the Claude Code CLI command with system prompt file support.
// This is the preferred variant for the universal agent runner.
func BuildPTYCommandWithPromptFile(prompt, systemPromptFile string, interactive bool) []string {
	if interactive {
		cmd := []string{"claude", "--dangerously-skip-permissions"}
		if systemPromptFile != "" {
			cmd = append(cmd, "--append-system-prompt-file", systemPromptFile)
		}
		return cmd
	}
	cmd := []string{"claude", "--dangerously-skip-permissions", "-p", prompt}
	if systemPromptFile != "" {
		cmd = append(cmd, "--append-system-prompt-file", systemPromptFile)
	}
	return cmd
}

// buildEnv constructs the environment variables for the claude subprocess.
func (c *ClaudeCLI) buildEnv() []string {
	env := os.Environ()
	env = append(env, BuildModelEnv(c.resolved, c.small)...)
	return env
}
