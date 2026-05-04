package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/scheduler"
	"github.com/xiii/orqestra/internal/tokenlimit"
	"github.com/xiii/orqestra/internal/types"
)

// Custom message types for TUI state transitions.

// PlanReadyMsg signals that planning completed successfully.
type PlanReadyMsg struct{}

// ConfirmChoice represents the user's decision at the Human Gate.
type ConfirmChoice int

const (
	ConfirmAccept ConfirmChoice = iota // user approved the plan
	ConfirmReject                      // user rejected the plan
	ConfirmEdit                        // user chose to save the plan for editing
)

// ConfirmMsg carries the user's gate decision.
type ConfirmMsg struct {
	Choice ConfirmChoice
}

// PlanSavedMsg is sent after the plan is successfully written to disk.
type PlanSavedMsg struct {
	FilePath string
}

// PromptSubmitMsg carries a user-typed prompt from the command bar.
type PromptSubmitMsg struct {
	Prompt string
}

// CommandMsg carries a parsed slash command from the command bar.
type CommandMsg struct {
	Name string
	Args string
}

// IntentResultMsg carries the structured intent recognition result.
type IntentResultMsg struct {
	Verdict                string
	Rephrased              string
	EndState               string
	Reason                 string
	Questions              []string
	ImprovedPromptExamples []string
	Err                    error
}

// IntentResult is the non-UI equivalent returned by the pipeline intake hook.
type IntentResult struct {
	Verdict                string
	Rephrased              string
	EndState               string
	Reason                 string
	Questions              []string
	ImprovedPromptExamples []string
}

// IntentConfirmMsg signals that the user approved the rephrased intent.
type IntentConfirmMsg struct{}

// IntentRejectMsg signals that the user rejected the rephrased intent.
type IntentRejectMsg struct{}

// ToggleLogsMsg signals that the log panel should be toggled.
type ToggleLogsMsg struct{}

// CycleBackToIdleMsg signals StateDone should transition back to StateIdle.
type CycleBackToIdleMsg struct{}

// PlanValidatedMsg signals that plan validation completed.
type PlanValidatedMsg struct {
	Report *types.ValidationReport
	Err    error
}

// ProjectPlanReadyMsg signals that the PM finished decomposing the spec.
type ProjectPlanReadyMsg struct {
	Plan types.ProjectPlan
	Err  error
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

// SchedulerEventMsg wraps a scheduler event for the TUI.
type SchedulerEventMsg struct {
	Event scheduler.Event
}

// TokenLimitExceededMsg signals that a model's token budget was exhausted.
type TokenLimitExceededMsg struct {
	Err *tokenlimit.ErrBudgetExhausted
}

// SandboxStateMsg signals a sandbox lifecycle state change.
type SandboxStateMsg struct {
	SandboxID string
	State     string // "pending", "provisioning", "ready", "running", "stopped", "extracting", "destroyed"
}

// CursorBlinkMsg is fired by the cursor blink tick loop in confirmView.
type CursorBlinkMsg struct{}

// ValidationStartedMsg signals that the HTTP work validator has started (triggers spinner).
type ValidationStartedMsg struct{}

// streamChunkCmd creates a tea.Cmd that emits a StreamChunkMsg.
func streamChunkCmd(tabIndex int, content string) tea.Cmd {
	return func() tea.Msg {
		return StreamChunkMsg{TabIndex: tabIndex, Content: content}
	}
}

// PulseTickMsg drives the tab pulsing animation.
type PulseTickMsg struct{}
