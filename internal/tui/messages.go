package tui

import (
	"time"

	"github.com/xiii/orqestra/internal/harness"
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

// intent is a marker interface for messages that flow from screens to the
// parent model. The parent handles them in the main Update() switch via the
// normal Bubbletea message loop — never by synchronously executing cmds.
type intent interface {
	isIntent()
}

// StartPipelineIntent requests the orchestrator to begin a new pipeline run.
type StartPipelineIntent struct {
	Prompt      string
	SkipGateway bool
}

func (StartPipelineIntent) isIntent() {}

// CancelPipelineIntent requests cancellation of the active pipeline run.
type CancelPipelineIntent struct{}

func (CancelPipelineIntent) isIntent() {}

// ApprovePlanIntent approves the current plan to proceed with execution.
type ApprovePlanIntent struct{}

func (ApprovePlanIntent) isIntent() {}

// EditPlanIntent submits a user-modified plan for re-validation.
type EditPlanIntent struct {
	ModifiedMarkdown string
}

func (EditPlanIntent) isIntent() {}

// CommentPlanIntent sends a comment to refine the current plan.
type CommentPlanIntent struct {
	Comment string
}

func (CommentPlanIntent) isIntent() {}

// CancelPlanIntent cancels the current plan review.
type CancelPlanIntent struct{}

func (CancelPlanIntent) isIntent() {}

// NavigateToPromptIntent requests navigation to the prompt input screen.
type NavigateToPromptIntent struct {
	PreFillGoal string
}

func (NavigateToPromptIntent) isIntent() {}

// NavigateToRunsListIntent requests navigation to the runs list screen.
type NavigateToRunsListIntent struct{}

func (NavigateToRunsListIntent) isIntent() {}

// NavigateToRunDetailIntent requests navigation to a specific run's detail view.
type NavigateToRunDetailIntent struct {
	RunIndex int
}

func (NavigateToRunDetailIntent) isIntent() {}

// NavigateBackIntent requests navigation to the previous screen.
type NavigateBackIntent struct{}

func (NavigateBackIntent) isIntent() {}

// ToggleDashboardIntent toggles the dashboard panel visibility.
type ToggleDashboardIntent struct{}

func (ToggleDashboardIntent) isIntent() {}

// OpenExternalEditorIntent opens a file in the user's external editor.
type OpenExternalEditorIntent struct {
	FilePath string
}

func (OpenExternalEditorIntent) isIntent() {}

// ConfirmNewRunIntent confirms the user wants to start a new run.
type ConfirmNewRunIntent struct{}

func (ConfirmNewRunIntent) isIntent() {}

// SubmitQuestionAnswerIntent submits a user's answer to an MCP AskUserQuestion.
type SubmitQuestionAnswerIntent struct {
	Answer harness.MCPAnswer
}

func (SubmitQuestionAnswerIntent) isIntent() {}

// AbortMergeIntent aborts the post-run merge (user chose not to resolve conflicts).
type AbortMergeIntent struct{}

func (AbortMergeIntent) isIntent() {}
