package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/types"
)

// PTYOutputMsg delivers raw bytes from a PTY session to the terminal view.
type PTYOutputMsg struct {
	TabIndex int
	Data     []byte
}

// PTYDoneMsg signals that a PTY session has exited.
type PTYDoneMsg struct {
	TabIndex int
	Err      error
	ExitCode int
}

// PTYNeedsInputMsg signals that the PTY session is waiting for user input.
type PTYNeedsInputMsg struct {
	TabIndex int
}

// AttentionMsg signals that a tab's agent needs user attention (BEL detected).
type AttentionMsg struct {
	TabIndex int
}

// IntakeCompleteMsg signals that the interactive intake runner finished.
type IntakeCompleteMsg struct {
	Artifact []byte
	Err      error
}

// ErrorMsg signals an unrecoverable error.
type ErrorMsg struct {
	Err error
}

// LogMsg delivers a log entry to the TUI log panel.
type LogMsg struct {
	Entry LogEntry
}

// SandboxStateMsg signals a sandbox lifecycle state change.
type SandboxStateMsg struct {
	SandboxID string
	State     string
}

// ToggleLogsMsg signals that the log panel should be toggled.
type ToggleLogsMsg struct{}

// PulseTickMsg drives the tab pulsing animation.
type PulseTickMsg struct{}

// attachPTYMsg delivers a live PTY writer to be attached to a term tab.
type attachPTYMsg struct {
	tabIndex int
	pty      PTYWriter
}

// setProgramMsg delivers the tea.Program reference to the model.
type setProgramMsg struct {
	program *tea.Program
}

// PTYWriter is the write-side of a PTY for sending user input to the agent
// and controlling terminal size.
type PTYWriter interface {
	Write(p []byte) (n int, err error)
	Resize(cols, rows uint) error
}

// WaitFunc blocks until an interactive agent exits and returns the artifact.
type WaitFunc func(ctx context.Context) ([]byte, error)

// PipelineFuncs holds the functions the TUI drives.
type PipelineFuncs struct {
	// LaunchInteractive starts an interactive Claude Code agent in a sandbox.
	// Returns a PTYWriter for bidirectional I/O and a WaitFunc that blocks
	// until the agent exits, returning the extracted artifact.
	LaunchInteractive func(ctx context.Context, prompt string, send func(tea.Msg), tabIndex int) (PTYWriter, WaitFunc, error)

	// Send delivers a tea.Msg into the TUI event loop. Wired after program creation.
	Send func(tea.Msg)

	// InitialSpec, when non-nil, signals a pre-loaded spec (e.g. resume from file).
	InitialSpec *types.Specification
}
