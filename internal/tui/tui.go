package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// Run launches the Bubble Tea TUI.
// The interactive agent auto-launches on startup. Returns on exit.
func Run(pipeline PipelineFuncs) error {
	m := NewModel(pipeline)

	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithOutput(os.Stderr),
	)

	// Redirect slog to the TUI log panel.
	logHandler := newTUILogHandler(p)
	slog.SetDefault(slog.New(logHandler))

	// Wire Send so sandbox callbacks can push state into the TUI.
	pipeline.Send = p.Send

	// Send program reference so model can start the agent.
	go p.Send(setProgramMsg{program: p})

	// Run with panic recovery.
	defer func() {
		if r := recover(); r != nil {
			p.Kill()
			fmt.Fprintf(os.Stderr, "TUI panic recovered: %v\n", r)
		}
	}()

	_, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// tuiLogHandler is a slog.Handler that sends log entries to the TUI.
type tuiLogHandler struct {
	program *tea.Program
	attrs   []slog.Attr
}

func newTUILogHandler(p *tea.Program) *tuiLogHandler {
	return &tuiLogHandler{program: p}
}

func (h *tuiLogHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *tuiLogHandler) Handle(_ context.Context, r slog.Record) error {
	entry := LogEntry{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   make(map[string]string),
	}

	for _, a := range h.attrs {
		entry.Attrs[a.Key] = a.Value.String()
	}

	r.Attrs(func(a slog.Attr) bool {
		entry.Attrs[a.Key] = a.Value.String()
		return true
	})

	h.program.Send(LogMsg{Entry: entry})
	return nil
}

func (h *tuiLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newH := &tuiLogHandler{
		program: h.program,
		attrs:   make([]slog.Attr, len(h.attrs)+len(attrs)),
	}
	copy(newH.attrs, h.attrs)
	copy(newH.attrs[len(h.attrs):], attrs)
	return newH
}

func (h *tuiLogHandler) WithGroup(_ string) slog.Handler {
	return h
}
