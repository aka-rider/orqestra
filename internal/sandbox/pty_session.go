package sandbox

import (
	"context"
	"fmt"
	"io"
	"sync"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

// SessionState represents the lifecycle state of a PTY session.
type SessionState int

const (
	SessionPending SessionState = iota
	SessionRunning
	SessionDone
	SessionFailed
)

// PTYSession provides bidirectional TTY I/O with a Docker container subprocess
// via the native Go SDK (ContainerExecCreate + ContainerExecAttach with Tty: true).
type PTYSession struct {
	ID   string
	Name string

	mu          sync.Mutex
	state       SessionState
	cli         *dockerclient.Client
	execID      string
	conn        *dockertypes.HijackedResponse
	cancel      context.CancelFunc
	exitCode    int
	exited      bool
	closed      bool
	cols, rows  uint
	containerID string
	eofCh       chan struct{} // closed when Read returns EOF
}

// NewPTYSession creates a new PTY session with the given Docker client.
func NewPTYSession(id, name string, cli *dockerclient.Client) *PTYSession {
	return &PTYSession{
		ID:    id,
		Name:  name,
		state: SessionPending,
		cli:   cli,
		eofCh: make(chan struct{}),
	}
}

// State returns the current session state.
func (ps *PTYSession) State() SessionState {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.state
}

// Start creates a Docker exec with TTY and attaches to it.
func (ps *PTYSession) Start(ctx context.Context, containerID string, command []string, env []string, cols, rows uint) error {
	ctx, ps.cancel = context.WithCancel(ctx)
	ps.containerID = containerID
	ps.cols = cols
	ps.rows = rows

	execCfg := container.ExecOptions{
		Cmd:          command,
		Env:          env,
		User:         "sandbox",
		WorkingDir:   "/workspace",
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := ps.cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		ps.mu.Lock()
		ps.state = SessionFailed
		ps.mu.Unlock()
		return fmt.Errorf("pty exec create: %w", err)
	}
	ps.execID = execResp.ID

	conn, err := ps.cli.ContainerExecAttach(ctx, ps.execID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		ps.mu.Lock()
		ps.state = SessionFailed
		ps.mu.Unlock()
		return fmt.Errorf("pty exec attach: %w", err)
	}
	ps.conn = &conn

	// Resize to requested dimensions immediately after attach.
	if cols > 0 && rows > 0 {
		_ = ps.cli.ContainerExecResize(ctx, ps.execID, container.ResizeOptions{
			Height: uint(rows),
			Width:  uint(cols),
		})
	}

	ps.mu.Lock()
	ps.state = SessionRunning
	ps.mu.Unlock()

	// Launch exit monitor goroutine — waits for EOF signal from Read, then inspects.
	go ps.monitorExit()

	return nil
}

// monitorExit waits for the stream EOF (signaled by Read), then inspects the exit code.
func (ps *PTYSession) monitorExit() {
	// Wait until the reader signals EOF.
	<-ps.eofCh

	// Inspect the exec process to retrieve the exit code.
	inspect, err := ps.cli.ContainerExecInspect(context.Background(), ps.execID)

	ps.mu.Lock()
	ps.exited = true
	if err == nil {
		ps.exitCode = inspect.ExitCode
	}
	if err != nil || inspect.ExitCode != 0 {
		ps.state = SessionFailed
	} else {
		ps.state = SessionDone
	}
	ps.mu.Unlock()
}

// Write sends bytes to the PTY stdin. Safe for concurrent use with Read.
func (ps *PTYSession) Write(p []byte) (int, error) {
	if ps.conn == nil {
		return 0, fmt.Errorf("pty session not started")
	}
	return ps.conn.Conn.Write(p)
}

// Read reads from the PTY stdout/stderr stream. Blocking. Returns io.EOF when
// the process exits and the Docker stream closes.
func (ps *PTYSession) Read(p []byte) (int, error) {
	if ps.conn == nil {
		return 0, fmt.Errorf("pty session not started")
	}
	n, err := ps.conn.Reader.Read(p)
	if err == io.EOF {
		// Signal the exit monitor that the stream ended.
		select {
		case <-ps.eofCh:
		default:
			close(ps.eofCh)
		}
	}
	return n, err
}

// Resize sends a terminal resize (SIGWINCH) to the container PTY.
func (ps *PTYSession) Resize(cols, rows uint) error {
	if ps.execID == "" {
		return fmt.Errorf("pty session not started")
	}
	ps.mu.Lock()
	ps.cols = cols
	ps.rows = rows
	ps.mu.Unlock()
	return ps.cli.ContainerExecResize(context.Background(), ps.execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

// ExitCode returns the process exit code. Valid after Read returns io.EOF.
func (ps *PTYSession) ExitCode() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.exitCode
}

// Close tears down the PTY session. Idempotent.
func (ps *PTYSession) Close() error {
	ps.mu.Lock()
	if ps.closed {
		ps.mu.Unlock()
		return nil
	}
	ps.closed = true
	ps.mu.Unlock()

	// Send SIGINT to request graceful termination.
	if ps.conn != nil {
		_, _ = ps.conn.Conn.Write([]byte{3}) // Ctrl+C
	}

	// Close the hijacked connection — unblocks pending Read/Write.
	if ps.conn != nil {
		ps.conn.Close()
	}

	// Signal EOF channel in case Read was never called or didn't see EOF.
	select {
	case <-ps.eofCh:
	default:
		close(ps.eofCh)
	}

	// Cancel the context.
	if ps.cancel != nil {
		ps.cancel()
	}

	return nil
}
