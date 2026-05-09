//go:build darwin

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/sandbox"
)

// SandboxCLIRunner implements CLIRunner by running the claude CLI
// directly on macOS under a sandbox (sandbox-exec) policy.
type SandboxCLIRunner struct {
	cfg      config.SandboxConfig
	profiles []sandbox.Snapshot
	repoPath string
	env      []string
	writable bool
}

// SandboxCLIRunnerConfig configures the seatbelt CLI runner.
type SandboxCLIRunnerConfig struct {
	Cfg      config.SandboxConfig
	Profiles []sandbox.Snapshot
	RepoPath string
	Env      []string // harness env (model routing)
	Writable bool     // true for workers
}

// NewSandboxCLIRunner creates a CLI runner backed by seatbelt.
func NewSandboxCLIRunner(cfg SandboxCLIRunnerConfig) *SandboxCLIRunner {
	return &SandboxCLIRunner{
		cfg:      cfg.Cfg,
		profiles: cfg.Profiles,
		repoPath: cfg.RepoPath,
		env:      cfg.Env,
		writable: cfg.Writable,
	}
}

// RunPrint runs the claude CLI with --print under seatbelt.
func (r *SandboxCLIRunner) RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error) {
	args := r.buildCommand(prompt, systemPrompt, false)
	output, err := r.run(ctx, args, nil)
	if err != nil {
		return RunResult{Output: output}, err
	}
	return RunResult{Output: output, Usage: extractJSONUsage(output), SessionID: extractStreamSessionID(output)}, nil
}

// RunStreaming runs the claude CLI with streaming output under seatbelt.
func (r *SandboxCLIRunner) RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (RunResult, error) {
	args := r.buildCommand(prompt, systemPrompt, true)
	output, err := r.run(ctx, args, stdout)
	if err != nil {
		return RunResult{Output: output}, err
	}
	return RunResult{Output: output, Usage: extractStreamUsage(output), SessionID: extractStreamSessionID(output)}, nil
}

// RunContinue resumes a previous session under seatbelt.
func (r *SandboxCLIRunner) RunContinue(ctx context.Context, sessionID, prompt string, stdout io.Writer) (RunResult, error) {
	args := []string{"claude", "--resume", sessionID, "--dangerously-skip-permissions", "-p", prompt, "--output-format", "stream-json", "--verbose"}
	output, err := r.run(ctx, args, stdout)
	if err != nil {
		return RunResult{Output: output}, err
	}
	return RunResult{Output: output, Usage: extractStreamUsage(output), SessionID: extractStreamSessionID(output)}, nil
}

func (r *SandboxCLIRunner) buildCommand(prompt, systemPrompt string, streaming bool) []string {
	args := []string{"claude", "--dangerously-skip-permissions", "-p", prompt}
	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	if streaming {
		args = append(args, "--output-format", "stream-json", "--verbose")
	} else {
		args = append(args, "--output-format", "json")
	}
	return args
}

func (r *SandboxCLIRunner) run(ctx context.Context, args []string, stdout io.Writer) (string, error) {
	sb, err := sandbox.New(sandbox.Config{
		RepoPath:     r.repoPath,
		RepoWritable: r.writable,
		Profiles:     r.profiles,
		HarnessEnv:   r.env,
		ProxyEnv:     r.cfg.ProxyEnv,
		ExtraEnv:     r.cfg.ExtraEnv,
	})
	if err != nil {
		return "", fmt.Errorf("sandbox cli runner: %w", err)
	}
	defer sb.Close()

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if err := sb.Wrap(cmd); err != nil {
		return "", fmt.Errorf("sandbox cli runner wrap: %w", err)
	}

	var outBuf bytes.Buffer
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(&outBuf, stdout)
	} else {
		cmd.Stdout = &outBuf
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	if err := sb.Run(ctx, cmd); err != nil {
		return outBuf.String(), fmt.Errorf("sandbox cli runner exec: %w (stderr: %s)", err, errBuf.String())
	}

	return outBuf.String(), nil
}

// extractJSONUsage parses token usage from a claude --output-format json response.
func extractJSONUsage(raw string) TokenUsage {
	var envelope struct {
		Usage *streamUsage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Usage != nil {
		return TokenUsage{
			InputTokens:  envelope.Usage.InputTokens,
			OutputTokens: envelope.Usage.OutputTokens,
			TotalTokens:  envelope.Usage.InputTokens + envelope.Usage.OutputTokens,
		}
	}
	return TokenUsage{}
}

// extractStreamUsage scans stream-json lines for the last result event with usage.
func extractStreamUsage(raw string) TokenUsage {
	var last TokenUsage
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type  string       `json:"type"`
			Usage *streamUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "result" && event.Usage != nil {
			last = TokenUsage{
				InputTokens:  event.Usage.InputTokens,
				OutputTokens: event.Usage.OutputTokens,
				TotalTokens:  event.Usage.InputTokens + event.Usage.OutputTokens,
			}
		}
	}
	return last
}

// extractStreamSessionID scans stream-json lines for a session_id in the result event.
func extractStreamSessionID(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
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
