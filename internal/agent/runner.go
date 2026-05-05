package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/xiii/orqestra/internal/sandbox"
)

// RunCallbacks receives lifecycle events from the agent runner.
// All callbacks are called from the runner goroutine — callers must ensure
// thread safety (e.g., use p.Send for Bubble Tea integration).
type RunCallbacks struct {
	// OnOutput delivers raw PTY output bytes.
	OnOutput func(data []byte)
	// OnBEL is called when a BEL character (\x07) is detected in the output.
	// The BEL is detected outside of OSC sequences to avoid false positives.
	OnBEL func()
	// OnDone is called when the PTY process exits.
	OnDone func(exitCode int, err error)
	// OnSandboxState is called when the sandbox lifecycle state changes.
	OnSandboxState func(sandboxID string, state sandbox.State)
}

// RunConfig configures a single agent execution.
type RunConfig struct {
	Spec      AgentSpec
	Session   SessionDir
	Sandbox   sandbox.Config
	RepoPath  string // host-side repo path for sandbox workspace
	Callbacks RunCallbacks
}

// Runner executes agents using the universal sandbox primitive.
type Runner struct{}

// NewRunner creates an agent runner.
func NewRunner() *Runner {
	return &Runner{}
}

// Run provisions a sandbox, stages files, launches the agent PTY, streams output,
// detects BEL signals, waits for exit, extracts the artifact, and destroys the sandbox.
// Returns the extracted artifact bytes on success.
func (r *Runner) Run(ctx context.Context, cfg RunConfig) ([]byte, error) {
	spec := cfg.Spec

	// Provision sandbox.
	sb := sandbox.NewDockerSandbox(cfg.Sandbox, cfg.RepoPath, spec.Env)
	r.emitState(cfg.Callbacks, sb.ID(), sandbox.StateProvisioning)

	defer func() {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sb.Destroy(destroyCtx); err != nil {
			slog.Error("agent: sandbox destroy failed", "id", sb.ID(), "err", err)
		}
		r.emitState(cfg.Callbacks, sb.ID(), sandbox.StateDestroyed)
	}()

	if err := sb.Provision(ctx); err != nil {
		return nil, fmt.Errorf("agent %s provision: %w", spec.Role, err)
	}
	r.emitState(cfg.Callbacks, sb.ID(), sandbox.StateReady)

	// Stage files: system prompt + input artifacts.
	files := r.buildStageFiles(spec)
	if len(files) > 0 {
		if err := sb.StageFiles(ctx, files); err != nil {
			return nil, fmt.Errorf("agent %s stage files: %w", spec.Role, err)
		}
	}

	// Launch PTY session.
	containerID := sb.ContainerID()
	if containerID == "" {
		return nil, fmt.Errorf("agent %s: sandbox not provisioned (no container ID)", spec.Role)
	}

	cli := sb.Client()
	if cli == nil {
		return nil, fmt.Errorf("agent %s: sandbox has no docker client", spec.Role)
	}

	sessName := fmt.Sprintf("%s-%d", spec.Role, time.Now().UnixMilli())
	pty := sandbox.NewPTYSession(sessName, string(spec.Role), cli)
	if err := pty.Start(ctx, containerID, spec.Command, spec.Env, 120, 40); err != nil {
		return nil, fmt.Errorf("agent %s pty start: %w", spec.Role, err)
	}
	defer pty.Close()

	r.emitState(cfg.Callbacks, sb.ID(), sandbox.StateRunning)

	// Stream PTY output, detecting BEL signals.
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.readLoop(pty, cfg.Callbacks)
	}()

	// Wait for PTY to close.
	<-done

	// Check exit code.
	exitCode := pty.ExitCode()
	if cfg.Callbacks.OnDone != nil {
		if exitCode != 0 {
			cfg.Callbacks.OnDone(exitCode, fmt.Errorf("agent %s exited with code %d", spec.Role, exitCode))
		} else {
			cfg.Callbacks.OnDone(exitCode, nil)
		}
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("agent %s exited with code %d", spec.Role, exitCode)
	}

	// Extract output artifact.
	if spec.OutputFile == "" {
		slog.Info("agent: no output file specified, skipping extraction", "role", spec.Role)
		return nil, nil
	}

	artifact, err := r.extractArtifact(ctx, sb, spec.OutputFile)
	if err != nil {
		return nil, fmt.Errorf("agent %s extract artifact: %w", spec.Role, err)
	}

	// Save to session directory.
	artifactName := fmt.Sprintf("%s.json", spec.Role)
	if err := cfg.Session.WriteArtifact(artifactName, artifact); err != nil {
		return nil, fmt.Errorf("agent %s save artifact: %w", spec.Role, err)
	}

	slog.Info("agent: completed", "role", spec.Role, "artifact_size", len(artifact))
	return artifact, nil
}

// buildStageFiles constructs the file map to stage into the sandbox.
func (r *Runner) buildStageFiles(spec AgentSpec) map[string][]byte {
	files := make(map[string][]byte)

	// System prompt as a file.
	if spec.SystemPrompt != "" {
		files["/workspace/.orqestra/agent/system-prompt.md"] = []byte(spec.SystemPrompt)
	}

	// Input files from prior phases.
	for name, content := range spec.InputFiles {
		path := "/workspace/.orqestra/agent/input/" + name
		files[path] = content
	}

	return files
}

// readLoop reads from the PTY, calling OnOutput for each chunk and OnBEL
// when standalone BEL characters are detected (not inside OSC sequences).
func (r *Runner) readLoop(pty *sandbox.PTYSession, cb RunCallbacks) {
	buf := make([]byte, 4096)
	for {
		n, err := pty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// Detect BEL characters outside OSC sequences.
			if cb.OnBEL != nil {
				r.scanForBEL(data, cb.OnBEL)
			}

			if cb.OnOutput != nil {
				cb.OnOutput(data)
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Warn("agent: pty read error", "err", err)
			}
			return
		}
	}
}

// scanForBEL scans a byte slice for standalone BEL (\x07) characters,
// excluding those that terminate OSC sequences (\x1b]...\x07).
func (r *Runner) scanForBEL(data []byte, onBEL func()) {
	inOSC := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		if b == 0x1b && i+1 < len(data) && data[i+1] == ']' {
			inOSC = true
			i++ // skip the ]
			continue
		}
		if b == 0x07 {
			if inOSC {
				// BEL terminates the OSC sequence — not a standalone signal.
				inOSC = false
			} else {
				onBEL()
			}
		}
		// ST (ESC \) also terminates OSC sequences.
		if inOSC && b == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			inOSC = false
			i++
		}
	}
}

// extractArtifact reads the output file from the sandbox.
func (r *Runner) extractArtifact(ctx context.Context, sb *sandbox.DockerSandbox, outputFile string) ([]byte, error) {
	// Use a temporary file to CopyOut, then read it.
	tmpFile := fmt.Sprintf("/tmp/orqestra-artifact-%d", time.Now().UnixNano())
	defer func() {
		_ = removeIfExists(tmpFile)
	}()

	if err := sb.CopyOut(ctx, outputFile, tmpFile); err != nil {
		return nil, fmt.Errorf("copy out %s: %w", outputFile, err)
	}

	data, err := readFileContent(tmpFile)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("output file %s is empty", outputFile)
	}

	return data, nil
}

func (r *Runner) emitState(cb RunCallbacks, sandboxID string, state sandbox.State) {
	if cb.OnSandboxState != nil {
		cb.OnSandboxState(sandboxID, state)
	}
}

func removeIfExists(path string) error {
	os.Remove(path)
	return nil
}

func readFileContent(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// LiveSession represents a running interactive agent session.
// The TUI attaches to it for bidirectional PTY I/O. When the session ends,
// call Wait() to extract the artifact and tear down the sandbox.
type LiveSession struct {
	pty     *sandbox.PTYSession
	sb      *sandbox.DockerSandbox
	spec    AgentSpec
	session SessionDir
	cb      RunCallbacks
	runner  *Runner
	done    chan struct{}
}

// PTY returns the PTY session for bidirectional I/O.
// Implements the PTYWriter interface (Write + Resize).
func (ls *LiveSession) PTY() *sandbox.PTYSession {
	return ls.pty
}

// Wait blocks until the agent exits, extracts the output artifact if configured,
// and tears down the sandbox. Returns the artifact bytes on success.
// Must be called exactly once.
func (ls *LiveSession) Wait(ctx context.Context) ([]byte, error) {
	// Wait for the read loop to finish (process exited).
	<-ls.done

	exitCode := ls.pty.ExitCode()
	if ls.cb.OnDone != nil {
		if exitCode != 0 {
			ls.cb.OnDone(exitCode, fmt.Errorf("agent %s exited with code %d", ls.spec.Role, exitCode))
		} else {
			ls.cb.OnDone(exitCode, nil)
		}
	}

	// Tear down sandbox after extraction.
	defer func() {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ls.sb.Destroy(destroyCtx); err != nil {
			slog.Error("agent: sandbox destroy failed", "id", ls.sb.ID(), "err", err)
		}
		ls.runner.emitState(ls.cb, ls.sb.ID(), sandbox.StateDestroyed)
	}()

	if exitCode != 0 {
		return nil, fmt.Errorf("agent %s exited with code %d", ls.spec.Role, exitCode)
	}

	// Extract output artifact.
	if ls.spec.OutputFile == "" {
		slog.Info("agent: no output file specified, skipping extraction", "role", ls.spec.Role)
		return nil, nil
	}

	artifact, err := ls.runner.extractArtifact(ctx, ls.sb, ls.spec.OutputFile)
	if err != nil {
		return nil, fmt.Errorf("agent %s extract artifact: %w", ls.spec.Role, err)
	}

	artifactName := fmt.Sprintf("%s.json", ls.spec.Role)
	if err := ls.session.WriteArtifact(artifactName, artifact); err != nil {
		return nil, fmt.Errorf("agent %s save artifact: %w", ls.spec.Role, err)
	}

	slog.Info("agent: completed", "role", ls.spec.Role, "artifact_size", len(artifact))
	return artifact, nil
}

// RunInteractive provisions a sandbox, stages files, launches the agent PTY in
// interactive mode, starts the read loop (streaming output + BEL detection), and
// returns a LiveSession immediately. The caller attaches the PTY to a TUI tab
// for bidirectional I/O and later calls Wait() for cleanup.
func (r *Runner) RunInteractive(ctx context.Context, cfg RunConfig) (*LiveSession, error) {
	spec := cfg.Spec

	sb := sandbox.NewDockerSandbox(cfg.Sandbox, cfg.RepoPath, spec.Env)
	r.emitState(cfg.Callbacks, sb.ID(), sandbox.StateProvisioning)

	if err := sb.Provision(ctx); err != nil {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sb.Destroy(destroyCtx)
		return nil, fmt.Errorf("agent %s provision: %w", spec.Role, err)
	}
	r.emitState(cfg.Callbacks, sb.ID(), sandbox.StateReady)

	// Stage files.
	files := r.buildStageFiles(spec)
	if len(files) > 0 {
		if err := sb.StageFiles(ctx, files); err != nil {
			destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sb.Destroy(destroyCtx)
			return nil, fmt.Errorf("agent %s stage files: %w", spec.Role, err)
		}
	}

	// Launch PTY.
	containerID := sb.ContainerID()
	if containerID == "" {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sb.Destroy(destroyCtx)
		return nil, fmt.Errorf("agent %s: sandbox not provisioned (no container ID)", spec.Role)
	}

	cli := sb.Client()
	if cli == nil {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sb.Destroy(destroyCtx)
		return nil, fmt.Errorf("agent %s: sandbox has no docker client", spec.Role)
	}

	sessName := fmt.Sprintf("%s-%d", spec.Role, time.Now().UnixMilli())
	pty := sandbox.NewPTYSession(sessName, string(spec.Role), cli)
	if err := pty.Start(ctx, containerID, spec.Command, spec.Env, 120, 40); err != nil {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sb.Destroy(destroyCtx)
		return nil, fmt.Errorf("agent %s pty start: %w", spec.Role, err)
	}

	r.emitState(cfg.Callbacks, sb.ID(), sandbox.StateRunning)

	// Start read loop in background.
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.readLoop(pty, cfg.Callbacks)
	}()

	return &LiveSession{
		pty:     pty,
		sb:      sb,
		spec:    spec,
		session: cfg.Session,
		cb:      cfg.Callbacks,
		runner:  r,
		done:    done,
	}, nil
}
