//go:build darwin

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
)

// OpenCodeCLI implements ContinuableRunner by wrapping the opencode CLI binary.
// It uses native provider semantics: no API key or model-routing env vars.
type OpenCodeCLI struct {
	binary    string   // path to opencode binary, defaults to "/opt/homebrew/bin/opencode"
	model     string   // e.g. "llama/qwen3.6-coder" — REQUIRED, no fallback
	agent     string   // e.g. "plan" or "build"
	sessionID string   // set after RunStreaming to track the session
	extraArgs []string // extra CLI flags appended to every invocation
	workDir   string   // working directory for subprocess; empty inherits process CWD
	pure      bool     // pass --pure to disable MCPs
}

// OpenCodeOption is a functional option for OpenCodeCLI.
type OpenCodeOption func(*OpenCodeCLI)

// WithOpenCodeModel sets the provider/model for opencode (e.g. "llama/qwen3.6-coder").
// This is required — opencode defaults to huggingface provider without it, which
// burns cloud tokens. Without explicit -m, opencode may silently fall back.
func WithOpenCodeModel(model string) OpenCodeOption {
	return func(c *OpenCodeCLI) {
		c.model = model
	}
}

// WithOpenCodeAgent sets the opencode agent mode ("plan" or "build").
func WithOpenCodeAgent(agent string) OpenCodeOption {
	return func(c *OpenCodeCLI) {
		c.agent = agent
	}
}

// WithOpenCodePure disables all plugins and MCPs by passing --pure.
func WithOpenCodePure(pure bool) OpenCodeOption {
	return func(c *OpenCodeCLI) {
		c.pure = pure
	}
}

// WithOpenCodeExtraArgs appends extra CLI flags to every opencode invocation.
func WithOpenCodeExtraArgs(args ...string) OpenCodeOption {
	return func(c *OpenCodeCLI) {
		c.extraArgs = append(c.extraArgs, args...)
	}
}

// WithOpenCodeWorkDir sets the working directory for the opencode subprocess.
func WithOpenCodeWorkDir(dir string) OpenCodeOption {
	return func(c *OpenCodeCLI) {
		c.workDir = dir
	}
}

// NewOpenCodeCLI creates a new OpenCodeCLI with the given binary path and options.
// If binaryPath is empty, defaults to "/opt/homebrew/bin/opencode".
func NewOpenCodeCLI(binaryPath string, opts ...OpenCodeOption) *OpenCodeCLI {
	if binaryPath == "" {
		binaryPath = "/opt/homebrew/bin/opencode"
	}
	c := &OpenCodeCLI{
		binary: binaryPath,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// compile-time interface check
var _ ContinuableRunner = (*OpenCodeCLI)(nil)

// RunPrint runs opencode synchronously with --format json.
func (c *OpenCodeCLI) RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error) {
	// Validate model before spawning — fail closed to prevent silent cloud fallback.
	if err := c.validateModel(); err != nil {
		return RunResult{}, err
	}

	args := c.buildArgs(prompt)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}
	cmd.Env = c.buildEnv()

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return RunResult{Output: outBuf.String()}, fmt.Errorf("opencode run print: %w (stderr: %s)", err, errBuf.String())
	}

	return RunResult{
		Output:    extractOpencodeResult(outBuf.String()),
		Usage:     extractOpencodeUsage(outBuf.String()),
		SessionID: extractOpencodeSessionID(outBuf.String()),
	}, nil
}

// RunStreaming runs opencode with --format json and parses the NDJSON stream.
func (c *OpenCodeCLI) RunStreaming(ctx context.Context, prompt, systemPrompt string, events chan<- StreamUpdate) (RunResult, error) {
	// Validate model before spawning — fail closed to prevent silent cloud fallback.
	if err := c.validateModel(); err != nil {
		return RunResult{}, err
	}

	args := c.buildArgs(prompt)

	cmdStdout, err := c.runStreaming(ctx, args, events)
	if err != nil {
		return RunResult{}, err
	}

	return RunResult{
		Output:    extractOpencodeResult(cmdStdout),
		Usage:     extractOpencodeUsage(cmdStdout),
		SessionID: extractOpencodeSessionID(cmdStdout),
	}, nil
}

func (c *OpenCodeCLI) runStreaming(ctx context.Context, args []string, events chan<- StreamUpdate) (string, error) {
	cmd := exec.CommandContext(ctx, c.binary, args...)
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}
	cmd.Env = c.buildEnv()

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("opencode stream stdout pipe: %w", err)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("opencode stream start: %w", err)
	}

	raw, scanErr := parseOpencodeStream(cmdStdout, events)
	if scanErr != nil {
		return raw, fmt.Errorf("opencode stream parse: %w", scanErr)
	}

	if err := cmd.Wait(); err != nil {
		return raw, fmt.Errorf("opencode stream exec: %w (stderr: %s)", err, errBuf.String())
	}
	return raw, nil
}

// RunContinue resumes an existing opencode session with -s <sessionID> and a new prompt.
func (c *OpenCodeCLI) RunContinue(ctx context.Context, sessionID, prompt string, events chan<- StreamUpdate) (RunResult, error) {
	// Validate model before spawning — fail closed to prevent silent cloud fallback.
	if err := c.validateModel(); err != nil {
		return RunResult{}, err
	}

	args := c.buildArgsWithSession(prompt, sessionID)

	cmdStdout, err := c.runStreaming(ctx, args, events)
	if err != nil {
		return RunResult{}, err
	}

	return RunResult{
		Output:    extractOpencodeResult(cmdStdout),
		Usage:     extractOpencodeUsage(cmdStdout),
		SessionID: sessionID, // retain the resumed session ID
	}, nil
}

// buildArgs constructs the CLI argument list for opencode run.
// The prompt is passed as a positional argument (message).
func (c *OpenCodeCLI) buildArgs(prompt string) []string {
	args := []string{"run", "--format", "json", "--dangerously-skip-permissions"}
	if c.model != "" {
		args = append(args, "-m", c.model)
	}
	if c.agent != "" {
		args = append(args, "--agent", c.agent)
	}
	if c.pure {
		args = append(args, "--pure")
	}
	args = append(args, c.extraArgs...)
	args = append(args, prompt)
	return args
}

// buildArgsWithSession constructs CLI args for session continuation with -s <sessionID>.
func (c *OpenCodeCLI) buildArgsWithSession(prompt, sessionID string) []string {
	args := []string{"run", "--format", "json", "--dangerously-skip-permissions", "-s", sessionID}
	if c.model != "" {
		args = append(args, "-m", c.model)
	}
	if c.agent != "" {
		args = append(args, "--agent", c.agent)
	}
	if c.pure {
		args = append(args, "--pure")
	}
	args = append(args, c.extraArgs...)
	args = append(args, prompt)
	return args
}

// buildEnv returns the process environment. OpenCode uses native semantics
// (no API key or model-routing env vars).
func (c *OpenCodeCLI) buildEnv() []string {
	return os.Environ()
}

// validateModel checks that a model is explicitly configured.
// Without explicit -m, opencode defaults to the huggingface provider which
// burns cloud tokens. This validation fails closed before spawning the process.
func (c *OpenCodeCLI) validateModel() error {
	if c.model == "" {
		return fmt.Errorf("opencode: model is required — provide WithOpenCodeModel (e.g. \"llama/qwen3.6-coder\"); without explicit -m opencode defaults to huggingface provider which burns cloud tokens")
	}
	return nil
}

// --- Opencode NDJSON parser ---

// opencodeEvent represents a parsed event from opencode's --format json output.
type opencodeEvent struct {
	Type      string           `json:"type"`
	Timestamp float64          `json:"timestamp,omitempty"`
	SessionID string           `json:"sessionID,omitempty"`
	Data      json.RawMessage  `json:"data,omitempty"`
	Part      opencodePart     `json:"part,omitempty"`
	Error     opencodeError    `json:"error,omitempty"`
}

// opencodePart holds the part data from opencode events.
// The part.type field distinguishes sub-types: "step-start", "text", "tool", "step-finish", "reasoning".
type opencodePart struct {
	ID        string  `json:"id,omitempty"`
	SessionID string  `json:"sessionID,omitempty"`
	MessageID string  `json:"messageID,omitempty"` //nolint:revive
	PartType  string  `json:"type,omitempty"`
	Text      string  `json:"text,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
	Tokens    *struct {
		Total      int64 `json:"total,omitempty"`
		Input      int64 `json:"input,omitempty"`
		Output     int64 `json:"output,omitempty"`
		Reasoning  int64 `json:"reasoning,omitempty"`
		CacheRead  int64 `json:"cache_read,omitempty"`
		CacheWrite int64 `json:"cache_write,omitempty"`
	} `json:"tokens,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	CallID    string          `json:"callID,omitempty"` //nolint:revive
	ToolState *struct {
		Status  string          `json:"status,omitempty"`
		Input   json.RawMessage `json:"input,omitempty"`
		Output  json.RawMessage `json:"output,omitempty"`
		Title   string          `json:"title,omitempty"`
	} `json:"state,omitempty"`
}

// opencodeError holds error data from session.error events.
type opencodeError struct {
	Name    string `json:"name,omitempty"`
	Message string `json:"message,omitempty"`
}

// parseOpencodeStream reads opencode --format json NDJSON from src line by line,
// routes each parsed event to events (nil-safe), and returns the raw output string.
//
// Event mapping (based on actual opencode v1.15.13 output):
//
//	step_start → StreamUpdate{Text: "step_start"}
//	text → StreamUpdate{Text: part.Text}
//	tool_use → StreamUpdate{Tool: part.Tool, Detail: part.state.title}
//	reasoning → StreamUpdate{Text: part.Text}
//	step_finish → StreamUpdate{Input: part.tokens.input, Output: part.tokens.output, UsageValid: true}
//	error → StreamUpdate{Detail: error message}
func parseOpencodeStream(r io.Reader, events chan<- StreamUpdate) (string, error) {
	var rawBuf bytes.Buffer
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, initialScanBufferBytes), maxJSONLLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		rawBuf.Write(line)
		rawBuf.WriteByte('\n')

		if len(line) == 0 {
			continue
		}

		var event opencodeEvent
		if err := json.Unmarshal(line, &event); err != nil {
			slog.Debug("non-JSON stream line from opencode", "err", err, "line_len", len(line))
			if events != nil {
				events <- StreamUpdate{Text: string(line) + "\n"}
			}
			continue
		}

		// Dispatch based on top-level event type.
		switch event.Type {
		case "step_start":
			if events != nil {
				events <- StreamUpdate{Text: "step_start"}
			}

		case "text":
			if event.Part.Text != "" && events != nil {
				events <- StreamUpdate{Text: event.Part.Text}
			}

		case "tool_use":
			if events != nil {
				tu := StreamUpdate{Tool: event.Part.Tool}
				if event.Part.ToolState != nil {
					tu.Detail = event.Part.ToolState.Title
				}
				events <- tu
			}

		case "reasoning":
			if event.Part.Text != "" && events != nil {
				events <- StreamUpdate{Text: event.Part.Text}
			}

		case "step_finish":
			// Tokens are in part.tokens (not a separate step field).
			if event.Part.Tokens != nil && event.Part.Tokens.Input+event.Part.Tokens.Output > 0 {
				if events != nil {
					events <- StreamUpdate{
						Input:      event.Part.Tokens.Input,
						Output:     event.Part.Tokens.Output,
						UsageValid: true,
					}
				}
			}

		case "error":
			if events != nil && event.Error.Message != "" {
				events <- StreamUpdate{Detail: fmt.Sprintf("opencode error: %s: %s", event.Error.Name, event.Error.Message)}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return rawBuf.String(), err
	}
	return rawBuf.String(), nil
}

// extractOpencodeResult scans NDJSON for text from "text" type events.
func extractOpencodeResult(raw string) string {
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event opencodeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "text" {
			b.WriteString(event.Part.Text)
		}
	}
	return b.String()
}

// extractOpencodeUsage scans NDJSON for the last step_finish event with token counts.
func extractOpencodeUsage(raw string) TokenUsage {
	var last TokenUsage
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event opencodeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "step_finish" && event.Part.Tokens != nil {
			if event.Part.Tokens.Input+event.Part.Tokens.Output > 0 {
				last = TokenUsage{
					Input:  event.Part.Tokens.Input,
					Output: event.Part.Tokens.Output,
				}
			}
		}
	}
	return last
}

// extractOpencodeSessionID scans NDJSON for the first non-empty sessionID.
func extractOpencodeSessionID(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			SessionID string `json:"sessionID"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.SessionID != "" {
			return event.SessionID
		}
	}
	return ""
}

// extractOpencodeJSONUsage parses token usage from a --format json (non-streaming) response.
func extractOpencodeJSONUsage(raw string) TokenUsage {
	var envelope struct {
		Usage *struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Usage != nil {
		return TokenUsage{
			Input:  envelope.Usage.Input,
			Output: envelope.Usage.Output,
		}
	}
	return TokenUsage{}
}
