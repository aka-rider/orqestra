//go:build darwin

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// NativePTY wraps a local PTY (creack/pty) for bidirectional I/O with a sandboxed process.
type NativePTY struct {
	ptmx *os.File
	cmd  *exec.Cmd
	mu   sync.Mutex
	exit int
	done bool
}

// StartNativePTY starts cmd under a PTY and returns the NativePTY handle.
// The cmd must already have Path, Args, Env, Dir configured
// (typically via seatbelt.Sandbox.Wrap). Note: Setpgid is cleared because
// creack/pty sets Setsid which is incompatible with Setpgid.
func StartNativePTY(cmd *exec.Cmd, cols, rows uint16) (*NativePTY, error) {
	// creack/pty needs Setsid (new session), which conflicts with Setpgid.
	// The process is still isolated via the new session.
	if cmd.SysProcAttr != nil {
		cmd.SysProcAttr.Setpgid = false
	}
	winSize := &pty.Winsize{Cols: cols, Rows: rows}
	ptmx, err := pty.StartWithSize(cmd, winSize)
	if err != nil {
		return nil, fmt.Errorf("start native pty: %w", err)
	}
	return &NativePTY{ptmx: ptmx, cmd: cmd}, nil
}

// Read reads from the PTY master.
func (p *NativePTY) Read(buf []byte) (int, error) {
	return p.ptmx.Read(buf)
}

// Write writes to the PTY master (sends input to the process).
func (p *NativePTY) Write(data []byte) (int, error) {
	return p.ptmx.Write(data)
}

// Resize changes the PTY window size.
func (p *NativePTY) Resize(cols, rows int) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

// Wait blocks until the process exits and records the exit code.
func (p *NativePTY) Wait() error {
	err := p.cmd.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = true
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				p.exit = status.ExitStatus()
			} else {
				p.exit = 1
			}
		} else {
			p.exit = 1
		}
	}
	return err
}

// ExitCode returns the process exit code. Only valid after Wait returns.
func (p *NativePTY) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit
}

// Close closes the PTY master file descriptor.
func (p *NativePTY) Close() error {
	return p.ptmx.Close()
}
