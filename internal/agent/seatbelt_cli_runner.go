//go:build darwin

package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/seatbelt"
)

// SeatbeltCLIRunner implements harness.CLIRunner by running the claude CLI
// directly on macOS under a seatbelt (sandbox-exec) policy.
// Unlike the Docker SandboxedCLIRunner, it does not need file extraction
// since workers write directly to the repo.
type SeatbeltCLIRunner struct {
	cfg      config.SeatbeltConfig
	profiles []seatbelt.Snapshot
	repoPath string
	env      []string
	writable bool // worker mode vs readonly
}

// SeatbeltCLIRunnerConfig configures the seatbelt CLI runner.
type SeatbeltCLIRunnerConfig struct {
	Cfg      config.SeatbeltConfig
	Profiles []seatbelt.Snapshot
	RepoPath string
	Env      []string // harness env (model routing)
	Writable bool     // true for workers
}

// NewSeatbeltCLIRunner creates a CLI runner backed by seatbelt.
func NewSeatbeltCLIRunner(cfg SeatbeltCLIRunnerConfig) *SeatbeltCLIRunner {
	return &SeatbeltCLIRunner{
		cfg:      cfg.Cfg,
		profiles: cfg.Profiles,
		repoPath: cfg.RepoPath,
		env:      cfg.Env,
		writable: cfg.Writable,
	}
}

// RunPrint runs the claude CLI with --print under seatbelt.
func (r *SeatbeltCLIRunner) RunPrint(ctx context.Context, prompt, systemPrompt string) (harness.RunResult, error) {
	args := r.buildCommand(prompt, systemPrompt, false)
	output, err := r.run(ctx, args, nil)
	return harness.RunResult{Output: output}, err
}

// RunStreaming runs the claude CLI with streaming output under seatbelt.
func (r *SeatbeltCLIRunner) RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (harness.RunResult, error) {
	args := r.buildCommand(prompt, systemPrompt, true)
	output, err := r.run(ctx, args, stdout)
	return harness.RunResult{Output: output}, err
}

func (r *SeatbeltCLIRunner) buildCommand(prompt, systemPrompt string, streaming bool) []string {
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

func (r *SeatbeltCLIRunner) run(ctx context.Context, args []string, stdout io.Writer) (string, error) {
	sb, err := seatbelt.New(seatbelt.Config{
		RepoPath:     r.repoPath,
		RepoWritable: r.writable,
		Profiles:     r.profiles,
		HarnessEnv:   r.env,
		ProxyEnv:     r.cfg.ProxyEnv,
		ExtraEnv:     r.cfg.ExtraEnv,
	})
	if err != nil {
		return "", fmt.Errorf("seatbelt cli runner: %w", err)
	}
	defer sb.Close()

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if err := sb.Wrap(cmd); err != nil {
		return "", fmt.Errorf("seatbelt cli runner wrap: %w", err)
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
		return outBuf.String(), fmt.Errorf("seatbelt cli runner exec: %w (stderr: %s)", err, errBuf.String())
	}

	return outBuf.String(), nil
}
