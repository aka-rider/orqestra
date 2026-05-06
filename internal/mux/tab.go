//go:build darwin

package mux

import (
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xiii/orqestra/internal/agent"
)

// TabState represents the lifecycle state of a tab.
type TabState int

const (
	TabRunning TabState = iota
	TabDone
)

// Tab represents a single PTY-backed agent session managed by the mux.
type Tab struct {
	Name      string
	Index     int
	PTY       *agent.NativePTY
	StartedAt time.Time
	State     TabState
	ExitCode  int

	// attention indicates this tab needs user attention (BEL detected).
	attention atomic.Bool

	// done is closed when the PTY process exits.
	done chan struct{}

	// mu protects state/exitCode.
	mu sync.Mutex
}

// NewTab creates a new tab wrapping an existing NativePTY.
func NewTab(name string, index int, pty *agent.NativePTY) *Tab {
	return &Tab{
		Name:      name,
		Index:     index,
		PTY:       pty,
		StartedAt: time.Now(),
		State:     TabRunning,
		done:      make(chan struct{}),
	}
}

// Done returns a channel that's closed when the tab's process exits.
func (t *Tab) Done() <-chan struct{} {
	return t.done
}

// NeedsAttention returns whether this tab has a pending BEL attention marker.
func (t *Tab) NeedsAttention() bool {
	return t.attention.Load()
}

// ClearAttention removes the attention marker.
func (t *Tab) ClearAttention() {
	t.attention.Store(false)
}

// Status returns the current state and exit code (thread-safe).
func (t *Tab) Status() (TabState, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.State, t.ExitCode
}

// ReadLoop continuously reads from the PTY. When this tab is the active tab
// (determined by the activeIdx pointer), output is written to the writer (stdout).
// When inactive, bytes are scanned for BEL and discarded. This prevents the
// background process from blocking on a full PTY buffer.
func (t *Tab) ReadLoop(w io.Writer, activeIdx *atomic.Int32) {
	buf := make([]byte, 4096)
	for {
		n, err := t.PTY.Read(buf)
		if n > 0 {
			data := buf[:n]

			// Always scan for BEL (even when active, for telemetry).
			scanForBEL(data, func() {
				t.attention.Store(true)
			})

			// Write to stdout only if this tab is active.
			if int(activeIdx.Load()) == t.Index {
				if _, writeErr := w.Write(data); writeErr != nil {
					slog.Warn("mux: stdout write error", "err", writeErr)
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Warn("mux: tab read error", "tab", t.Name, "err", err)
			}
			break
		}
	}

	// Wait for process exit and record state.
	_ = t.PTY.Wait() // fire-and-forget: exit code captured by NativePTY internally
	t.mu.Lock()
	t.State = TabDone
	t.ExitCode = t.PTY.ExitCode()
	t.mu.Unlock()
	close(t.done)
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
