package sandbox

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// OnStateFunc is called when the sandbox state changes.
type OnStateFunc func(sandboxID string, state State)

// RunnerConfig configures a SandboxedCLIRunner.
type RunnerConfig struct {
	Sandbox  Config
	RepoPath string   // absolute path to the repo on the host
	Env      []string // environment variables to pass to the container
	Model    string   // model identifier for the claude CLI --model flag

	// OnState is called when the sandbox state changes.
	// May be nil.
	OnState OnStateFunc

	// StagingDir is the directory where extracted files are staged before
	// being copied to the repo. If empty, a temp directory is used.
	StagingDir string

	// SandboxFactory creates a new Sandbox instance. If nil, NewDockerSandbox is used.
	// Override in tests to inject a mock sandbox without requiring Docker.
	SandboxFactory func(cfg Config, repoPath string, env []string) Sandbox
}

// SandboxedCLIRunner implements harness.CLIRunner by running the claude CLI
// inside a Docker sandbox. After execution, it extracts changed files,
// verifies them, and copies them to the host repo.
type SandboxedCLIRunner struct {
	cfg      RunnerConfig
	verifier *Verifier
}

// NewSandboxedCLIRunner creates a runner that executes inside Docker sandboxes.
func NewSandboxedCLIRunner(cfg RunnerConfig) *SandboxedCLIRunner {
	return &SandboxedCLIRunner{
		cfg: cfg,
		verifier: NewVerifier(VerifierConfig{
			AllowedExecutables: cfg.Sandbox.AllowedExecutables,
			MaxFileSize:        100 * 1024 * 1024, // 100MB default
		}),
	}
}

// RunPrint runs the claude CLI with --print inside a sandbox.
func (r *SandboxedCLIRunner) RunPrint(ctx context.Context, prompt, systemPrompt string) (harness.RunResult, error) {
	command := r.buildCommand(prompt, systemPrompt, false)
	output, _, err := r.runInSandbox(ctx, command, nil)
	return harness.RunResult{Output: output}, err
}

// RunStreaming runs the claude CLI with streaming output inside a sandbox.
func (r *SandboxedCLIRunner) RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (harness.RunResult, error) {
	command := r.buildCommand(prompt, systemPrompt, true)
	output, _, err := r.runInSandbox(ctx, command, stdout)
	return harness.RunResult{Output: output}, err
}

// runInSandbox manages the full sandbox lifecycle: provision → exec → extract → verify → apply → destroy.
func (r *SandboxedCLIRunner) runInSandbox(ctx context.Context, command []string, stdout io.Writer) (string, []ChangedFile, error) {
	factory := r.cfg.SandboxFactory
	if factory == nil {
		factory = func(cfg Config, repoPath string, env []string) Sandbox {
			return NewDockerSandbox(cfg, repoPath, env)
		}
	}
	sb := factory(r.cfg.Sandbox, r.cfg.RepoPath, r.cfg.Env)
	r.emitState(sb.ID(), StatePending)

	// Always destroy the sandbox, even on error or panic.
	defer func() {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sb.Destroy(destroyCtx); err != nil {
			slog.Error("sandbox: destroy failed", "id", sb.ID(), "err", err)
		}
		r.emitState(sb.ID(), StateDestroyed)
	}()

	// Provision.
	r.emitState(sb.ID(), StateProvisioning)
	if err := sb.Provision(ctx); err != nil {
		return "", nil, fmt.Errorf("sandbox provision: %w", err)
	}
	r.emitState(sb.ID(), StateReady)

	// Exec.
	r.emitState(sb.ID(), StateRunning)

	// Capture output if no writer provided.
	var capture *captureWriter
	if stdout == nil {
		capture = &captureWriter{}
		stdout = capture
	} else {
		capture = &captureWriter{passthrough: stdout}
		stdout = capture
	}

	exitCode, err := sb.Exec(ctx, command, r.cfg.Env, stdout)
	r.emitState(sb.ID(), StateStopped)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox exec: %w", err)
	}
	if exitCode != 0 {
		return capture.String(), nil, fmt.Errorf("sandbox exec: exit code %d", exitCode)
	}

	// Extract changes.
	r.emitState(sb.ID(), StateExtracting)
	changes, err := sb.ExtractChanges(ctx)
	if err != nil {
		return capture.String(), nil, fmt.Errorf("sandbox extract: %w", err)
	}

	if len(changes) == 0 {
		slog.Info("sandbox: no file changes detected", "id", sb.ID())
		return capture.String(), nil, nil
	}

	// Verify extracted files.
	result := r.verifier.Verify(changes)
	if !result.Passed {
		slog.Error("sandbox: security verification failed",
			"id", sb.ID(),
			"rejected", len(result.Rejected),
		)
		for _, rej := range result.Rejected {
			slog.Error("sandbox: rejected file", "path", rej.Path, "reason", rej.Reason, "detail", rej.Detail)
		}
		return capture.String(), nil, fmt.Errorf("security verification failed: %d files rejected", len(result.Rejected))
	}

	// Copy verified files to staging, then to repo.
	if err := r.applyChanges(ctx, sb, changes); err != nil {
		return capture.String(), nil, fmt.Errorf("applying changes: %w", err)
	}

	slog.Info("sandbox: changes applied", "id", sb.ID(), "files", len(changes))
	return capture.String(), changes, nil
}

// applyChanges stages verified files from the sandbox to a staging directory,
// then copies them atomically to the host repo. This two-phase approach ensures
// the repo is not partially modified if any copy from the sandbox fails.
func (r *SandboxedCLIRunner) applyChanges(ctx context.Context, sb Sandbox, changes []ChangedFile) error {
	stagingDir := r.cfg.StagingDir
	if stagingDir == "" {
		var err error
		stagingDir, err = os.MkdirTemp("", "orqestra-staging-*")
		if err != nil {
			return fmt.Errorf("creating staging dir: %w", err)
		}
		defer os.RemoveAll(stagingDir)
	}

	// Phase 1: stage all added/modified files from the sandbox.
	// If any extraction fails, the repo is untouched.
	for _, f := range changes {
		if f.Op == FileDeleted {
			continue
		}
		stagingPath := filepath.Join(stagingDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(stagingPath), 0o755); err != nil {
			return fmt.Errorf("creating staging parent for %s: %w", f.Path, err)
		}
		if err := sb.CopyOut(ctx, f.Path, stagingPath); err != nil {
			return fmt.Errorf("staging %s: %w", f.Path, err)
		}
		slog.Debug("sandbox: staged", "path", f.Path)
	}

	// Phase 2: apply staged files to the repo.
	for _, f := range changes {
		switch f.Op {
		case FileDeleted:
			hostPath := filepath.Join(r.cfg.RepoPath, f.Path)
			if err := os.Remove(hostPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing %s: %w", f.Path, err)
			}
			slog.Debug("sandbox: deleted", "path", f.Path)

		case FileAdded, FileModified:
			stagingPath := filepath.Join(stagingDir, f.Path)
			hostPath := filepath.Join(r.cfg.RepoPath, f.Path)
			if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", f.Path, err)
			}
			if err := copyFile(stagingPath, hostPath); err != nil {
				return fmt.Errorf("applying %s: %w", f.Path, err)
			}
			slog.Debug("sandbox: applied", "path", f.Path, "op", f.Op)
		}
	}
	return nil
}

// copyFile copies the file at src to dst, overwriting dst if it exists.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// buildCommand constructs the claude CLI command to run inside the container.
func (r *SandboxedCLIRunner) buildCommand(prompt, systemPrompt string, streaming bool) []string {
	args := []string{"claude"}
	if streaming {
		args = append(args, "-p", prompt, "--output-format", "stream-json", "--verbose")
	} else {
		args = append(args, "--print", "-p", prompt, "--output-format", "json")
	}

	// Route to specific model if configured.
	if r.cfg.Model != "" {
		args = append(args, "--model", r.cfg.Model)
	}

	// Prepend sandbox environment context to the system prompt.
	fullSystemPrompt := sandboxSystemPrompt + "\n\n" + systemPrompt
	args = append(args, "--system-prompt", fullSystemPrompt)

	// Skip permissions — sandbox provides the isolation boundary.
	args = append(args, "--dangerously-skip-permissions")

	return args
}

// sandboxSystemPrompt describes the sandbox environment to the agent.
const sandboxSystemPrompt = `You are running inside an isolated Docker sandbox.

Environment:
- Working directory: /workspace (writable CoW snapshot of the source repo)
- MCP servers: Available via Docker's MCP gateway at /run/mcp.sock
  Use MCP tools normally — they are routed through Docker's native MCP integration.
- Network: Outbound access is available for package installs and API calls.
- Filesystem: /workspace is your writable workspace. All changes are tracked.
  Files outside /workspace are read-only.
- Dependencies: Heavy dependency directories (node_modules, vendor, etc.) may be
  symlinked from /deps/ into /workspace as read-only mounts.

Constraints:
- Do NOT modify files outside /workspace.
- All file changes will be security-verified before being applied to the host repo.
- Executable files will be rejected unless explicitly allowed by the specification.`

func (r *SandboxedCLIRunner) emitState(id string, state State) {
	if r.cfg.OnState != nil {
		r.cfg.OnState(id, state)
	}
}

// captureWriter captures output while optionally passing it through.
type captureWriter struct {
	passthrough io.Writer
	buf         []byte
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	if c.passthrough != nil {
		return c.passthrough.Write(p)
	}
	return len(p), nil
}

func (c *captureWriter) String() string {
	return string(c.buf)
}
