//go:build darwin

package mux

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// handleSignals registers OS signal handlers for the mux.
// SIGWINCH is forwarded to the active child PTY.
func (m *Mux) handleSignals(ctx context.Context) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	go func() {
		for {
			select {
			case <-ctx.Done():
				signal.Stop(winch)
				return
			case <-winch:
				m.resizeActive()
			}
		}
	}()
}

// resizeActive reads the current tty size and propagates it to the active tab PTY.
func (m *Mux) resizeActive() {
	if m.tty == nil {
		return
	}

	ws, err := unix.IoctlGetWinsize(int(m.tty.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		slog.Warn("mux: get winsize", "err", err)
		return
	}

	active := int(m.activeIdx.Load())
	m.mu.Lock()
	if active >= 0 && active < len(m.tabs) {
		tab := m.tabs[active]
		m.mu.Unlock()
		if err := pty.Setsize(tab.PTY.Fd(), &pty.Winsize{
			Cols: ws.Col,
			Rows: ws.Row,
		}); err != nil {
			slog.Warn("mux: resize active pty", "err", err)
		}
	} else {
		m.mu.Unlock()
	}
}
