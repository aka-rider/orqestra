package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
)

// Custom message types for TUI state transitions.

// PlanReadyMsg signals that planning completed successfully.
type PlanReadyMsg struct{}

// ConfirmMsg carries the user's approval decision.
type ConfirmMsg struct {
	Approved bool
}

// PlanValidatedMsg signals that plan validation completed.
type PlanValidatedMsg struct {
	Report *types.ValidationReport
	Err    error
}

// WorkValidatedMsg signals that work validation completed.
type WorkValidatedMsg struct {
	Report *types.ValidationReport
	Err    error
}

// StreamChunkMsg carries incremental output from a harness session.
type StreamChunkMsg struct {
	TabIndex  int
	SessionID string // if set, used to resolve tab index from sessionTabs
	Content   string
}

// HarnessDoneMsg signals that a harness session completed.
type HarnessDoneMsg struct {
	TabIndex   int
	Err        error
	WorkOutput string // captured work output for validation
}

// ErrorMsg signals an unrecoverable error.
type ErrorMsg struct {
	Err error
}

// TabSwitchMsg requests switching to a specific tab.
type TabSwitchMsg struct {
	Index int
}

// LogMsg delivers a log entry to the TUI log panel.
type LogMsg struct {
	Entry LogEntry
}

// SessionEventMsg wraps a harness.SessionEvent for the TUI event loop.
type SessionEventMsg struct {
	Event harness.SessionEvent
}

// streamChunkCmd creates a tea.Cmd that emits a StreamChunkMsg.
func streamChunkCmd(tabIndex int, content string) tea.Cmd {
	return func() tea.Msg {
		return StreamChunkMsg{TabIndex: tabIndex, Content: content}
	}
}
