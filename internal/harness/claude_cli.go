package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/xiii/orqestra/internal/config"
)

// CLIRunner is the interface for running claude CLI commands.
type CLIRunner interface {
	RunPrint(ctx context.Context, prompt, systemPrompt string) (string, error)
	RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (string, error)
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
func (c *ClaudeCLI) RunPrint(ctx context.Context, prompt, systemPrompt string) (string, error) {
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
		return "", fmt.Errorf("claude CLI error: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

// RunStreaming runs `claude -p <prompt> --system-prompt <systemPrompt> --output-format stream-json --verbose`
// and streams displayable content to stdout. Returns the final result text.
func (c *ClaudeCLI) RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (string, error) {
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
		return "", fmt.Errorf("claude CLI stdout pipe error: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("claude CLI start error: %w", err)
	}

	var result string
	scanner := bufio.NewScanner(cmdStdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large lines
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Not valid JSON — write raw to stdout
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
			// system, tool_use, tool_result, etc. — skip for display
		}
	}

	cmdErr := cmd.Wait()
	if cmdErr != nil {
		return "", fmt.Errorf("claude CLI error: %w (stderr: %s)", cmdErr, stderr.String())
	}

	if result == "" {
		return "", fmt.Errorf("claude CLI produced no result message in stream")
	}

	return result, nil
}

// streamEvent represents a parsed event from Claude CLI's stream-json output.
type streamEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Result  string          `json:"result,omitempty"`
	Delta   streamDeltaText `json:"delta,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
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

// buildEnv constructs the environment variables for the claude subprocess.
func (c *ClaudeCLI) buildEnv() []string {
	env := os.Environ()

	switch c.resolved.Type {
	case "anthropic":
		env = append(env,
			"ANTHROPIC_BASE_URL="+c.resolved.BaseURL,
			"ANTHROPIC_AUTH_TOKEN="+c.resolved.APIKey,
			"ANTHROPIC_MODEL="+c.resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL="+c.resolved.Model,
		)
		if c.small != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+c.small.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+c.small.Model,
			)
		}
	case "openai":
		baseURL := c.resolved.BaseURL
		if baseURL != "" && baseURL[len(baseURL)-1] != '/' {
			baseURL += "/v1"
		} else {
			baseURL += "v1"
		}
		env = append(env,
			"OPENAI_BASE_URL="+baseURL,
			"OPENAI_API_KEY="+c.resolved.APIKey,
		)
	}

	// Always inject operational flags
	env = append(env,
		"DISABLE_NON_ESSENTIAL_MODEL_CALLS=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)

	return env
}
