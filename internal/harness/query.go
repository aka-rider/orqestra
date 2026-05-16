package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// QueryRunner is the programmatic Query API for invoking Claude with full control
// over turns, tools, and continuation.
type QueryRunner interface {
	Query(ctx context.Context, cfg QueryConfig) (<-chan StreamEvent, error)
}

// QueryConfig controls a single Claude invocation.
type QueryConfig struct {
	Prompt          string
	SystemPrompt    string
	SessionID       string
	MaxTurns        int
	AllowedTools    []string
	DisallowedTools []string
	WorkDir         string
	Env             []string
	Binary          string
}

// StreamEvent is the interface for typed stream events from Claude CLI.
type StreamEvent interface{ streamEvent() }

// TextDelta is emitted for each chunk of assistant text.
type TextDelta struct{ Text string }

// ToolUse is emitted when the assistant invokes a tool.
type ToolUse struct {
	Name string
	Args json.RawMessage
}

// ToolResult is emitted when a tool returns output.
type ToolResult struct {
	Name        string
	Output      string
	TokensAdded int64
}

// UsageDelta reports token consumption.
type UsageDelta struct {
	InputTokens  int64
	OutputTokens int64
}

// Result is the final event indicating completion.
type Result struct {
	SessionID string
	Output    string
	Usage     TokenUsage
}

// ErrorEvent wraps an error encountered during streaming.
type ErrorEvent struct{ Err error }

func (TextDelta) streamEvent()  {}
func (ToolUse) streamEvent()    {}
func (ToolResult) streamEvent() {}
func (UsageDelta) streamEvent() {}
func (Result) streamEvent()     {}
func (ErrorEvent) streamEvent() {}

// AgentStats tracks live metrics for a running agent.
type AgentStats struct {
	AgentID      string
	Phase        string
	InputTokens  int64
	OutputTokens int64
	ThroughputPS float64
	CtxPercent   float64
	ToolCalls    []ToolCallSummary
}

// ToolCallSummary is a condensed record of a tool invocation.
type ToolCallSummary struct {
	Name     string
	Duration string
}

// CLIQueryRunner implements QueryRunner using the claude CLI binary.
type CLIQueryRunner struct {
	binary string
}

// NewCLIQueryRunner creates a QueryRunner backed by the claude CLI.
func NewCLIQueryRunner(binary string) *CLIQueryRunner {
	if binary == "" {
		binary = "claude"
	}
	return &CLIQueryRunner{binary: binary}
}

// Query launches the claude CLI with --print --output-format stream-json and streams events.
func (r *CLIQueryRunner) Query(ctx context.Context, cfg QueryConfig) (<-chan StreamEvent, error) {
	args := []string{"--print", "-p", cfg.Prompt, "--output-format", "stream-json"}

	if cfg.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}
	if cfg.SessionID != "" {
		args = append(args, "--continue", cfg.SessionID)
	}
	if cfg.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", cfg.MaxTurns))
	}
	for _, tool := range cfg.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}
	for _, tool := range cfg.DisallowedTools {
		args = append(args, "--disallowedTools", tool)
	}

	binary := r.binary
	if cfg.Binary != "" {
		binary = cfg.Binary
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	if len(cfg.Env) > 0 {
		cmd.Env = cfg.Env
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("query stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("query start: %w", err)
	}

	ch := make(chan StreamEvent, 64)

	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var raw struct {
				Type    string          `json:"type"`
				Result  string          `json:"result,omitempty"`
				Delta   json.RawMessage `json:"delta,omitempty"`
				Message json.RawMessage `json:"message,omitempty"`
				Usage   *streamUsage    `json:"usage,omitempty"`
			}
			if err := json.Unmarshal(line, &raw); err != nil {
				ch <- ErrorEvent{Err: fmt.Errorf("parse stream event: %w", err)}
				continue
			}

			switch raw.Type {
			case "content_block_delta":
				var delta struct {
					Text string `json:"text"`
				}
				if raw.Delta != nil {
					json.Unmarshal(raw.Delta, &delta)
				}
				if delta.Text != "" {
					ch <- TextDelta{Text: delta.Text}
				}
			case "tool_use":
				var tu struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"input"`
				}
				if raw.Message != nil {
					json.Unmarshal(raw.Message, &tu)
				}
				ch <- ToolUse{Name: tu.Name, Args: tu.Args}
			case "tool_result":
				var tr struct {
					Name   string `json:"name"`
					Output string `json:"output"`
				}
				if raw.Message != nil {
					json.Unmarshal(raw.Message, &tr)
				}
				ch <- ToolResult{Name: tr.Name, Output: tr.Output}
			case "result":
				var usage TokenUsage
				if raw.Usage != nil {
					usage = TokenUsage{
						InputTokens:  raw.Usage.InputTokens,
						OutputTokens: raw.Usage.OutputTokens,
						TotalTokens:  raw.Usage.InputTokens + raw.Usage.OutputTokens,
					}
				}
				ch <- Result{Output: raw.Result, Usage: usage}
			}

			if raw.Usage != nil {
				ch <- UsageDelta{
					InputTokens:  raw.Usage.InputTokens,
					OutputTokens: raw.Usage.OutputTokens,
				}
			}
		}

		if err := cmd.Wait(); err != nil {
			ch <- ErrorEvent{Err: fmt.Errorf("query process: %w", err)}
		}
	}()

	return ch, nil
}
