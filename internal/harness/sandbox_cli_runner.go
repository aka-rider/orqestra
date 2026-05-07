//go:build darwin

package harness

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

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
	return RunResult{Output: output}, err
}

// RunStreaming runs the claude CLI with streaming output under seatbelt.
func (r *SandboxCLIRunner) RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (RunResult, error) {
	args := r.buildCommand(prompt, systemPrompt, true)
	output, err := r.run(ctx, args, stdout)
	return RunResult{Output: output}, err
}

func (r *SandboxCLIRunner) buildCommand(prompt, systemPrompt string, streaming bool) []string {
	args := []string{"claude", "--dangerously-skip-permissions", "-p", prompt}
	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	if streaming {
		args = append(args, "--output-format", "stream-json")
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
