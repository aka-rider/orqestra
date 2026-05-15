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
	"os/exec"
	"strings"
	"syscall"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/sandbox"
)

// SandboxCLIRunner implements CLIRunner by running the claude CLI
// directly on macOS under a sandbox (sandbox-exec) policy.
type SandboxCLIRunner struct {
	cfg          config.SandboxConfig
	profiles     []sandbox.Snapshot
	repoPath     string
	worktreePath string // if set, worker runs in this worktree (repo stays read-only)
	env          []string
	writable     bool
}

// SandboxCLIRunnerConfig configures the seatbelt CLI runner.
type SandboxCLIRunnerConfig struct {
	Cfg          config.SandboxConfig
	Profiles     []sandbox.Snapshot
	RepoPath     string
	WorktreePath string   // optional worktree path; when set repo is read-only and worktree is read-write
	Env          []string // harness env (model routing)
	Writable     bool     // true for workers
}

// NewSandboxCLIRunner creates a CLI runner backed by seatbelt.
func NewSandboxCLIRunner(cfg SandboxCLIRunnerConfig) *SandboxCLIRunner {
	return &SandboxCLIRunner{
		cfg:          cfg.Cfg,
		profiles:     cfg.Profiles,
		repoPath:     cfg.RepoPath,
		worktreePath: cfg.WorktreePath,
		env:          cfg.Env,
		writable:     cfg.Writable,
	}
}

// WithWorktree returns a new SandboxCLIRunner configured to execute inside the
// given worktree path. The main repo becomes read-only; the worktree is read-write.
// This is used by the orchestrator after creating the per-run git worktree.
func (r *SandboxCLIRunner) WithWorktree(path string) *SandboxCLIRunner {
	copy := *r
	copy.worktreePath = path
	copy.writable = false // repo is read-only when a worktree is set
	return &copy
}
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
	output, err := r.runParsed(ctx, args, stdout)
	if err != nil {
		return RunResult{Output: output}, err
	}
	return RunResult{Output: output, Usage: extractStreamUsage(output), SessionID: extractStreamSessionID(output)}, nil
}

// RunContinue resumes a previous session under seatbelt.
func (r *SandboxCLIRunner) RunContinue(ctx context.Context, sessionID, prompt string, stdout io.Writer) (RunResult, error) {
	args := []string{"claude", "--resume", sessionID, "--dangerously-skip-permissions", "-p", prompt, "--output-format", "stream-json", "--verbose"}
	output, err := r.runParsed(ctx, args, stdout)
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
		WorktreePath: r.worktreePath,
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

// runParsed executes the sandboxed command, parses each stream-json line,
// writes human-readable text and tool activities to display, and returns
// the full raw NDJSON output for post-processing (usage + session ID extraction).
// display may be nil; dispatchStreamEvent handles nil safely.
func (r *SandboxCLIRunner) runParsed(ctx context.Context, args []string, display io.Writer) (string, error) {
	sb, err := sandbox.New(sandbox.Config{
		RepoPath:     r.repoPath,
		RepoWritable: r.writable,
		WorktreePath: r.worktreePath,
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
	// Wrap applies sandbox-exec trampoline, env, Setpgid=true, and working dir.
	// StdoutPipe must be called after Wrap (Wrap does not touch cmd.Stdout).
	if err := sb.Wrap(cmd); err != nil {
		return "", fmt.Errorf("sandbox cli runner wrap: %w", err)
	}

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("sandbox cli runner stdout pipe: %w", err)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("sandbox cli runner start: %w", err)
	}

	// Kill the process group on context cancellation.
	// Wrap sets Setpgid=true, so -cmd.Process.Pid kills all children.
	// The done channel prevents a goroutine leak when the process finishes normally.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // fire-and-forget: best-effort cleanup after caller cancellation
			}
		case <-done:
		}
	}()

	var rawBuf bytes.Buffer
	scanner := bufio.NewScanner(cmdStdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		rawBuf.Write(line)
		rawBuf.WriteByte('\n')
		if len(line) == 0 {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			slog.Debug("non-JSON stream line from sandbox cli", "line_len", len(line))
			if display != nil {
				display.Write(line)         // nolint:errcheck
				display.Write([]byte("\n")) // nolint:errcheck
			}
			continue
		}
		dispatchStreamEvent(event, display) // nil-safe: dispatchStreamEvent guards display == nil
	}
	if err := scanner.Err(); err != nil {
		return rawBuf.String(), fmt.Errorf("sandbox cli runner scan: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return rawBuf.String(), fmt.Errorf("sandbox cli runner exec: %w (stderr: %s)", err, errBuf.String())
	}
	return rawBuf.String(), nil
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
