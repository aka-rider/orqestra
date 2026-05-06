//go:build darwin

package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/seatbelt"
	"golang.org/x/sys/unix"
)

// AgentRunner is the interface for running sandboxed agents.
// Both Docker and seatbelt runners implement this.
type AgentRunner interface {
	Run(context.Context, RunConfig) ([]byte, error)
	RunInteractive(context.Context, RunConfig) (*NativeLiveSession, error)
}

// SeatbeltRunner executes agents using macOS-native sandbox-exec.
type SeatbeltRunner struct {
	cfg      config.SeatbeltConfig
	profiles []seatbelt.Snapshot
}

// NewSeatbeltRunner creates an agent runner backed by seatbelt (sandbox-exec).
func NewSeatbeltRunner(cfg config.SeatbeltConfig, profiles []seatbelt.Snapshot) (*SeatbeltRunner, error) {
	return &SeatbeltRunner{cfg: cfg, profiles: profiles}, nil
}

// NativeLiveSession represents a running interactive agent under seatbelt.
type NativeLiveSession struct {
	pty     *NativePTY
	sb      *seatbelt.Sandbox
	spec    AgentSpec
	session SessionDir
	cb      RunCallbacks
	done    chan struct{}
}

// PTY returns the native PTY for bidirectional I/O.
func (ls *NativeLiveSession) PTY() *NativePTY {
	return ls.pty
}

// Write implements PTYWriter for TUI integration.
func (ls *NativeLiveSession) Write(data []byte) (int, error) {
	return ls.pty.Write(data)
}

// Resize implements tui.PTYWriter for TUI integration.
func (ls *NativeLiveSession) Resize(cols, rows uint) error {
	return ls.pty.Resize(int(cols), int(rows))
}

// Wait blocks until the agent exits, reads artifacts from the session directory,
// and cleans up the sandbox. Must be called exactly once.
func (ls *NativeLiveSession) Wait(ctx context.Context) ([]byte, error) {
	<-ls.done

	exitCode := ls.pty.ExitCode()
	if ls.cb.OnDone != nil {
		if exitCode != 0 {
			ls.cb.OnDone(exitCode, fmt.Errorf("agent %s exited with code %d", ls.spec.Role, exitCode))
		} else {
			ls.cb.OnDone(exitCode, nil)
		}
	}

	// Cleanup sandbox profile temp file.
	defer ls.sb.Close()

	if exitCode != 0 {
		return nil, fmt.Errorf("agent %s exited with code %d", ls.spec.Role, exitCode)
	}

	// Read output artifact directly from session (no CopyOut needed).
	if ls.spec.OutputFile == "" {
		return nil, nil
	}

	artifactPath := filepath.Join(ls.session.Path, string(ls.spec.Role), "output", ls.spec.OutputFile)
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("agent %s read artifact %s: %w", ls.spec.Role, artifactPath, err)
	}

	// Persist normalized artifact.
	artifactName := fmt.Sprintf("%s.json", ls.spec.Role)
	if err := ls.session.WriteArtifact(artifactName, data); err != nil {
		return nil, fmt.Errorf("agent %s save artifact: %w", ls.spec.Role, err)
	}

	return data, nil
}

// Run executes a non-interactive agent in the seatbelt sandbox.
func (r *SeatbeltRunner) Run(ctx context.Context, cfg RunConfig) ([]byte, error) {
	ls, err := r.start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return ls.Wait(ctx)
}

// RunInteractive starts an interactive agent session under seatbelt and returns immediately.
// Unlike Run, the caller (mux) owns PTY reading. No internal read loop is started.
func (r *SeatbeltRunner) RunInteractive(ctx context.Context, cfg RunConfig) (*NativeLiveSession, error) {
	return r.startInteractive(ctx, cfg)
}

func (r *SeatbeltRunner) start(ctx context.Context, cfg RunConfig) (*NativeLiveSession, error) {
	return r.doStart(ctx, cfg, true)
}

func (r *SeatbeltRunner) startInteractive(ctx context.Context, cfg RunConfig) (*NativeLiveSession, error) {
	return r.doStart(ctx, cfg, false)
}

func (r *SeatbeltRunner) doStart(ctx context.Context, cfg RunConfig, withReadLoop bool) (*NativeLiveSession, error) {
	spec := cfg.Spec

	// Determine repo writability from role.
	repoWritable := spec.Role == RoleWorker

	// Ensure session role directories exist.
	roleDir := filepath.Join(cfg.Session.Path, string(spec.Role))
	inputDir := filepath.Join(roleDir, "input")
	outputDir := filepath.Join(roleDir, "output")
	for _, d := range []string{inputDir, outputDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("agent %s mkdir %s: %w", spec.Role, d, err)
		}
	}

	// Stage system prompt.
	if spec.SystemPrompt != "" {
		promptFile := filepath.Join(roleDir, "agent.md")
		if err := os.WriteFile(promptFile, []byte(spec.SystemPrompt), 0o644); err != nil {
			return nil, fmt.Errorf("agent %s write system prompt: %w", spec.Role, err)
		}
	}

	// Stage input files.
	for name, content := range spec.InputFiles {
		path := filepath.Join(inputDir, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return nil, fmt.Errorf("agent %s stage input %s: %w", spec.Role, name, err)
		}
	}

	// Create seatbelt sandbox.
	sb, err := seatbelt.New(seatbelt.Config{
		RepoPath:     cfg.RepoPath,
		SessionPath:  cfg.Session.Path,
		RepoWritable: repoWritable,
		Profiles:     r.profiles,
		HarnessEnv:   spec.Env,
		ProxyEnv:     r.cfg.ProxyEnv,
		ExtraEnv:     r.cfg.ExtraEnv,
	})
	if err != nil {
		return nil, fmt.Errorf("agent %s seatbelt: %w", spec.Role, err)
	}

	if cfg.Callbacks.OnState != nil {
		cfg.Callbacks.OnState(StateStarting)
	}

	// Build the command.
	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)

	// Wrap with seatbelt (sets env, pgid, sandbox-exec trampoline).
	if err := sb.Wrap(cmd); err != nil {
		sb.Close()
		return nil, fmt.Errorf("agent %s wrap: %w", spec.Role, err)
	}

	// Start under native PTY with real terminal size.
	cols, rows := getTerminalSize()
	npty, err := StartNativePTY(cmd, cols, rows)
	if err != nil {
		sb.Close()
		return nil, fmt.Errorf("agent %s pty start: %w", spec.Role, err)
	}

	if cfg.Callbacks.OnState != nil {
		cfg.Callbacks.OnState(StateRunning)
	}

	done := make(chan struct{})
	if withReadLoop {
		// Start read loop (non-interactive: runner owns PTY reading).
		go func() {
			defer close(done)
			r.readLoop(npty, cfg.Callbacks)
			npty.Wait()
		}()
	} else {
		// Interactive: mux owns PTY reading. Just monitor process exit.
		go func() {
			defer close(done)
			npty.Wait()
		}()
	}

	// Handle context cancellation in background.
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
			// Kill the process group.
			if npty.cmd.Process != nil {
				_ = syscall.Kill(-npty.cmd.Process.Pid, syscall.SIGKILL) // fire-and-forget: best-effort cleanup after caller cancellation
			}
		}
	}()

	return &NativeLiveSession{
		pty:     npty,
		sb:      sb,
		spec:    spec,
		session: cfg.Session,
		cb:      cfg.Callbacks,
		done:    done,
	}, nil
}

// readLoop reads from the PTY, calling OnOutput and scanning for BEL.
func (r *SeatbeltRunner) readLoop(pty *NativePTY, cb RunCallbacks) {
	buf := make([]byte, 4096)
	for {
		n, err := pty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			if cb.OnBEL != nil {
				scanForBEL(data, cb.OnBEL)
			}
			if cb.OnOutput != nil {
				cb.OnOutput(data)
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Warn("agent: native pty read error", "err", err)
			}
			return
		}
	}
}

// scanForBEL scans for standalone BEL (\x07) characters outside OSC sequences.
func scanForBEL(data []byte, onBEL func()) {
	inOSC := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		if b == 0x1b && i+1 < len(data) && data[i+1] == ']' {
			inOSC = true
			i++
			continue
		}
		if b == 0x07 {
			if inOSC {
				inOSC = false
			} else {
				onBEL()
			}
		}
		if inOSC && b == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			inOSC = false
			i++
		}
	}
}

// MaxLifetime returns the configured max lifetime, falling back to a generous default.
func (r *SeatbeltRunner) MaxLifetime() time.Duration {
	if r.cfg.MaxLifetime.Duration > 0 {
		return r.cfg.MaxLifetime.Duration
	}
	return 2 * time.Hour
}

// getTerminalSize queries /dev/tty for the current terminal dimensions.
// Falls back to 120x40 if unavailable.
func getTerminalSize() (cols, rows uint16) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return 120, 40
	}
	defer tty.Close()

	ws, err := unix.IoctlGetWinsize(int(tty.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 120, 40
	}
	return ws.Col, ws.Row
}
