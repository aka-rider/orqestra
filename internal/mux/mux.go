//go:build darwin

// Package mux implements a raw terminal passthrough multiplexer.
// The active tab owns the terminal completely — raw stdin→PTY, raw PTY→stdout.
// A configurable prefix key (default Ctrl+B) suspends passthrough and enters
// chrome mode (handled externally via the OnChrome callback).
package mux

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/xiii/orqestra/internal/agent"
)

// InputMode represents whether the mux is passing through to a PTY or showing chrome.
type InputMode int

const (
	ModeTerminal InputMode = iota
	ModeChrome
)

// TabExitedEvent is sent when a tab's process exits.
type TabExitedEvent struct {
	Index    int
	ExitCode int
}

// Config configures the mux.
type Config struct {
	// PrefixKey is the byte that triggers chrome mode. Default: 0x02 (Ctrl+B).
	PrefixKey byte

	// OnChrome is called when the prefix key is pressed. The mux pauses passthrough,
	// restores terminal state, and calls this function. It should return the index
	// of the tab to switch to (or -1 to quit). The mux resumes passthrough after
	// this function returns.
	OnChrome func(m *Mux) (newActive int, quit bool)

	// OnTabExited is called when a tab's process exits. Called from the tab's
	// read goroutine — must be safe for concurrent use.
	OnTabExited func(event TabExitedEvent)
}

// Mux is the core passthrough terminal multiplexer.
type Mux struct {
	cfg       Config
	tty       *os.File
	tabs      []*Tab
	activeIdx atomic.Int32
	mode      InputMode
	mu        sync.Mutex

	// ready is closed once Run() has opened /dev/tty and is ready for tabs.
	ready chan struct{}

	// lastChromeExit debounces rapid Ctrl+B presses.
	lastChromeExit time.Time
}

// New creates a new mux with the given configuration.
func New(cfg Config) *Mux {
	if cfg.PrefixKey == 0 {
		cfg.PrefixKey = 0x02 // Ctrl+B
	}
	m := &Mux{
		cfg:   cfg,
		ready: make(chan struct{}),
	}
	return m
}

// TTY returns the underlying tty file (available after Run starts).
func (m *Mux) TTY() *os.File {
	return m.tty
}

// AddTab registers a new tab with the mux. The tab's read loop is started
// immediately. Blocks until the mux is ready (Run has opened /dev/tty).
// Returns the tab index.
func (m *Mux) AddTab(name string, pty *agent.NativePTY) int {
	// Wait for Run() to open tty.
	<-m.ready

	m.mu.Lock()
	idx := len(m.tabs)
	tab := NewTab(name, idx, pty)
	m.tabs = append(m.tabs, tab)
	m.mu.Unlock()

	// Start read loop — writes to tty when active, discards when inactive.
	go func() {
		tab.ReadLoop(m.tty, &m.activeIdx)
		// Notify orchestrator of tab exit.
		if m.cfg.OnTabExited != nil {
			m.cfg.OnTabExited(TabExitedEvent{
				Index:    tab.Index,
				ExitCode: tab.ExitCode,
			})
		}
	}()

	return idx
}

// Active returns the currently active tab index.
func (m *Mux) Active() int {
	return int(m.activeIdx.Load())
}

// SetActive switches the active tab and sends SIGWINCH to the new child
// so it repaints.
func (m *Mux) SetActive(idx int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx < 0 || idx >= len(m.tabs) {
		return
	}
	m.activeIdx.Store(int32(idx))

	// Clear attention on the newly focused tab.
	m.tabs[idx].ClearAttention()

	// Send SIGWINCH to force repaint.
	m.sendWinch(m.tabs[idx])
}

// Tabs returns a snapshot of the current tabs (for chrome rendering).
func (m *Mux) Tabs() []*Tab {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Tab, len(m.tabs))
	copy(out, m.tabs)
	return out
}

// TabCount returns the number of tabs.
func (m *Mux) TabCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tabs)
}

// Run is the blocking main loop. It opens /dev/tty, puts it in raw mode,
// and starts the passthrough. Returns when context is cancelled or quit is requested.
func (m *Mux) Run(ctx context.Context) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("mux: open /dev/tty: %w", err)
	}
	defer tty.Close()
	m.tty = tty

	// Signal that tty is ready — unblocks AddTab callers.
	close(m.ready)

	// Show a brief startup indicator before entering raw mode.
	fmt.Fprintf(tty, "\x1b[2m⏳ orqestra: launching agent...\x1b[0m\r")

	// Register SIGWINCH handler.
	m.handleSignals(ctx)

	return m.passthrough(ctx)
}

// passthrough is the raw I/O loop that reads stdin and forwards to the active PTY.
func (m *Mux) passthrough(ctx context.Context) error {
	fd := m.tty.Fd()
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("mux: make raw: %w", err)
	}
	defer term.Restore(fd, oldState)

	buf := make([]byte, 4096)
	for {
		// Check context before blocking read.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := m.tty.Read(buf)
		if err != nil {
			return fmt.Errorf("mux: tty read: %w", err)
		}
		if n == 0 {
			continue
		}

		data := buf[:n]

		// Bracketed paste: if the buffer contains the paste start sequence,
		// don't intercept any prefix bytes — forward the entire buffer.
		if bytes.Contains(data, []byte("\x1b[200~")) {
			m.writeToActive(data)
			continue
		}

		// Prefix key detection: check for both legacy encoding (0x02) and
		// Kitty keyboard protocol encoding (ESC[98;5u for Ctrl+B).
		// Claude Code enables Kitty protocol (\x1b[?2031h), which changes
		// Ctrl+B from raw 0x02 to the CSI u sequence.
		prefixDetected := false
		if data[0] == m.cfg.PrefixKey {
			prefixDetected = true
		} else if m.cfg.PrefixKey == 0x02 && isPrefixKittySeq(data) {
			prefixDetected = true
		}

		if prefixDetected {
			// Calculate how many bytes the prefix key consumed.
			prefixLen := 1 // legacy 0x02
			if data[0] == 0x1b {
				// Kitty sequence — find the 'u' terminator.
				for i := 2; i < n; i++ {
					if data[i] == 'u' {
						prefixLen = i + 1
						break
					}
				}
			}

			// Double-press: if chrome was just exited <300ms ago, send the literal
			// byte to the child instead of re-entering chrome.
			if time.Since(m.lastChromeExit) < 300*time.Millisecond {
				m.writeToActive(data[:prefixLen])
				continue
			}
			// Forward any trailing data after the prefix sequence to the child.
			if n > prefixLen {
				m.writeToActive(data[prefixLen:])
			}
			quit := m.enterChrome(fd, oldState)
			if quit {
				return nil
			}
			continue
		}

		// Forward raw bytes to the active PTY.
		m.writeToActive(data)
	}
}

// enterChrome suspends passthrough, restores terminal, runs chrome, then resumes.
func (m *Mux) enterChrome(fd uintptr, rawState *term.State) (quit bool) {
	if m.cfg.OnChrome == nil {
		return false
	}

	// Disable Kitty keyboard protocol before entering chrome.
	// BubbleTea uses legacy input encoding.
	fmt.Fprintf(m.tty, "\x1b[?2031l")

	// Restore terminal to cooked mode for chrome UI.
	term.Restore(fd, rawState)

	m.mode = ModeChrome
	newActive, shouldQuit := m.cfg.OnChrome(m)
	m.mode = ModeTerminal
	m.lastChromeExit = time.Now()

	if shouldQuit {
		return true
	}

	// Switch tab if requested.
	if newActive >= 0 {
		m.SetActive(newActive)
	}

	// Re-enter raw mode.
	newRaw, err := term.MakeRaw(fd)
	if err != nil {
		slog.Error("mux: re-enter raw mode failed", "err", err)
		return true
	}
	// Update the outer rawState so defer Restore uses the latest.
	*rawState = *newRaw

	// SIGWINCH the active child to force repaint.
	m.mu.Lock()
	active := int(m.activeIdx.Load())
	if active >= 0 && active < len(m.tabs) {
		m.sendWinch(m.tabs[active])
	}
	m.mu.Unlock()

	return false
}

// writeToActive forwards data to the active tab's PTY.
func (m *Mux) writeToActive(data []byte) {
	active := int(m.activeIdx.Load())
	m.mu.Lock()
	if active >= 0 && active < len(m.tabs) {
		tab := m.tabs[active]
		m.mu.Unlock()
		if _, err := tab.PTY.Write(data); err != nil {
			slog.Warn("mux: write to active pty", "err", err, "tab", tab.Name)
		}
	} else {
		m.mu.Unlock()
	}
}

// sendWinch sends SIGWINCH to the tab's process to trigger a terminal repaint.
func (m *Mux) sendWinch(tab *Tab) {
	if tab.PTY == nil {
		return
	}
	pid := tab.PTY.Pid()
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGWINCH) // fire-and-forget: best-effort repaint signal
	}
}

// Shutdown gracefully terminates all child processes.
func (m *Mux) Shutdown() {
	m.mu.Lock()
	tabs := make([]*Tab, len(m.tabs))
	copy(tabs, m.tabs)
	m.mu.Unlock()

	// SIGTERM all children.
	for _, tab := range tabs {
		tab.mu.Lock()
		state := tab.State
		tab.mu.Unlock()
		if state == TabRunning {
			pid := tab.PTY.Pid()
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGTERM) // fire-and-forget: graceful shutdown attempt
			}
		}
	}

	// Wait up to 3s for graceful exit.
	deadline := time.After(3 * time.Second)
	for _, tab := range tabs {
		tab.mu.Lock()
		state := tab.State
		tab.mu.Unlock()
		if state == TabRunning {
			select {
			case <-tab.Done():
			case <-deadline:
				// Force kill.
				pid := tab.PTY.Pid()
				if pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL) // fire-and-forget: forced cleanup
				}
			}
		}
	}
}

// isPrefixKittySeq checks if the data starts with the Kitty keyboard protocol
// encoding for Ctrl+B: ESC [ 98 ; 5 u (codepoint 98 = 'b', modifier 5 = ctrl).
// Also matches the variant with release/repeat events: ESC [ 98 ; 5 : N u
func isPrefixKittySeq(data []byte) bool {
	// Minimum: \x1b [ 9 8 ; 5 u = 7 bytes
	if len(data) < 7 {
		return false
	}
	// Must start with CSI: ESC [
	if data[0] != 0x1b || data[1] != '[' {
		return false
	}
	// Match: 98;5u or 98;5:1u (press) or 98;5:2u (repeat)
	return bytes.HasPrefix(data[2:], []byte("98;5u")) ||
		bytes.HasPrefix(data[2:], []byte("98;5:1u")) ||
		bytes.HasPrefix(data[2:], []byte("98;5:2u"))
}
