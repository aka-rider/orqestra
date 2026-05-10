package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// --- TUI Messages (tea.Msg types) ---

// OrchestratorEventMsg wraps an orchestrator event for the TUI.
type OrchestratorEventMsg struct{ Event orchestrator.Event }

// tickMsg fires every second to refresh elapsed timers and live output.
type tickMsg time.Time

// filePickerBatchMsg carries a batch of discovered file/dir paths from the async walker.
type filePickerBatchMsg struct{ entries []string }

// filePickerDoneMsg signals that the async repo walk has completed.
type filePickerDoneMsg struct{}

// --- Intent Messages (screens → parent via tea.Cmd) ---

// StartPipelineIntent requests the orchestrator to begin a new pipeline run.
type StartPipelineIntent struct {
	Prompt      string
	SkipGateway bool
}

// CancelPipelineIntent requests cancellation of the active pipeline run.
type CancelPipelineIntent struct{}

// SubmitGatewayIntent submits the user's answers to gateway coaching questions.
type SubmitGatewayIntent struct {
	Answers []orchestrator.GatewayAnswer
}

// SkipGatewayIntent skips the gateway coaching step entirely.
type SkipGatewayIntent struct{}

// ApprovePlanIntent approves the current plan to proceed with execution.
type ApprovePlanIntent struct{}

// EditPlanIntent submits a user-modified plan for re-validation.
type EditPlanIntent struct {
	ModifiedMarkdown string
}

// CommentPlanIntent sends a comment to refine the current plan.
type CommentPlanIntent struct {
	Comment string
}

// CancelPlanIntent cancels the current plan review.
type CancelPlanIntent struct{}

// NavigateToPromptIntent requests navigation to the prompt input screen.
type NavigateToPromptIntent struct {
	PreFillGoal string
}

// NavigateToRunsListIntent requests navigation to the runs list screen.
type NavigateToRunsListIntent struct{}

// NavigateToRunDetailIntent requests navigation to a specific run's detail view.
type NavigateToRunDetailIntent struct {
	RunIndex int
}

// NavigateBackIntent requests navigation to the previous screen.
type NavigateBackIntent struct{}

// ToggleDashboardIntent toggles the dashboard panel visibility.
type ToggleDashboardIntent struct{}

// OpenExternalEditorIntent opens a file in the user's external editor.
type OpenExternalEditorIntent struct {
	FilePath string
}

// ConfirmNewRunIntent confirms the user wants to start a new run.
type ConfirmNewRunIntent struct{}

// intentCmd wraps a message as a tea.Cmd for intent dispatch.
func intentCmd(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return msg }
}
