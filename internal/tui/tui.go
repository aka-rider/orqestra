package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
)

// Run launches the Bubble Tea TUI and drives the full pipeline.
// The SessionManager's notify callback is wired to push events into the TUI.
// Sessions auto-create tabs — planning and execution are both driven as sessions.
//
// Returns only on /quit or ctrl+c. The TUI owns its lifecycle.
func Run(pipeline PipelineFuncs) error {
	m := NewModel(pipeline)

	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithOutput(os.Stderr),
	)

	// Redirect slog to the TUI log panel
	logHandler := newTUILogHandler(p)
	slog.SetDefault(slog.New(logHandler))

	// Wire the session manager's notifications into the TUI event loop
	if pipeline.SessionManager != nil {
		WireSessionManager(p, pipeline.SessionManager)
	}

	// Wire Send so sandbox callbacks can push state into the TUI.
	pipeline.Send = p.Send

	// Send program reference so model can start execution later.
	// Must be in a goroutine — Send blocks on unbuffered channel until Run starts.
	go p.Send(setProgramMsg{program: p})

	// Run with panic recovery
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

// WireSessionManager connects a SessionManager's event notifications to the
// tea.Program so that session lifecycle changes appear as tabs in the TUI.
func WireSessionManager(p *tea.Program, sm *harness.SessionManager) {
	sm.SetNotify(func(evt harness.SessionEvent) {
		p.Send(SessionEventMsg{Event: evt})
	})
}

// StartExecutionSession starts the worker session via the SessionManager,
// streaming into the TUI. Called from model.Update after confirmation.
func StartExecutionSession(p *tea.Program, ctx context.Context, sm *harness.SessionManager, spec types.Specification, pipeline PipelineFuncs) {
	// Build the prompt from spec
	execPrompt := fmt.Sprintf("Execute the following plan:\n\nGoal: %s\n\nSteps:\n", spec.Goal)
	for i, step := range spec.Steps {
		execPrompt += fmt.Sprintf("%d. %s\n", i+1, step)
	}

	// The session manager will emit SessionPending → TUI creates tab.
	// We use a sessionWriter that starts with tabIndex from the session event.
	sw := &sessionWriter{program: p, sessionID: "exec"}

	// Start the session — this runs the harness in a goroutine.
	// The SessionPending event will fire synchronously before run starts.
	sm.StartSession(ctx, "Worker", execPrompt, "", sw)
}

// addTabMsg is sent from goroutines to create a new tab (legacy, non-session path).
type addTabMsg struct {
	name string
}

// planCompleteMsg carries the parsed spec from the planning goroutine.
type planCompleteMsg struct {
	spec types.Specification
}

// setProgramMsg delivers the tea.Program reference to the model.
type setProgramMsg struct {
	program *tea.Program
}

// programWriter implements io.Writer and sends StreamChunkMsg by tab index.
type programWriter struct {
	program  *tea.Program
	tabIndex int
}

func (pw *programWriter) Write(p []byte) (n int, err error) {
	if len(p) > 0 {
		pw.program.Send(StreamChunkMsg{
			TabIndex: pw.tabIndex,
			Content:  string(p),
		})
	}
	return len(p), nil
}

// sessionWriter implements io.Writer and sends StreamChunkMsg keyed by session ID.
// The model resolves the session ID to a tab index at receive time.
type sessionWriter struct {
	program   *tea.Program
	sessionID string
}

func (sw *sessionWriter) Write(p []byte) (n int, err error) {
	if len(p) > 0 {
		sw.program.Send(StreamChunkMsg{
			SessionID: sw.sessionID,
			Content:   string(p),
		})
	}
	return len(p), nil
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

// RunExecution is the legacy execution path (no session manager).
func RunExecution(p *tea.Program, ctx context.Context, pipeline PipelineFuncs, spec types.Specification, tabIndex int) {
	cw := &capturingWriter{program: p, tabIndex: tabIndex}
	err := pipeline.Execute(ctx, spec, cw)
	p.Send(HarnessDoneMsg{TabIndex: tabIndex, Err: err, WorkOutput: cw.captured.String()})
}

var _ io.Writer = (*programWriter)(nil)
var _ io.Writer = (*sessionWriter)(nil)
var _ io.Writer = (*capturingWriter)(nil)

// capturingWriter wraps programWriter and also captures all output for work validation.
type capturingWriter struct {
	program  *tea.Program
	tabIndex int
	captured strings.Builder
}

func (cw *capturingWriter) Write(p []byte) (n int, err error) {
	if len(p) > 0 {
		cw.program.Send(StreamChunkMsg{
			TabIndex: cw.tabIndex,
			Content:  string(p),
		})
		cw.captured.Write(p)
	}
	return len(p), nil
}
