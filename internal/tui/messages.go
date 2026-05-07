package tui

import (
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// Screen identifies the current TUI view.
type Screen int

const (
	ScreenPrompt Screen = iota
	ScreenClarification
	ScreenDashboard
	ScreenAgentDetail
	ScreenPlanReview
	ScreenFailure
	ScreenQAResult
	ScreenCompletion
)

// --- TUI Messages (tea.Msg types) ---

// SubmitPromptMsg is sent when the user submits a prompt.
type SubmitPromptMsg struct{ Prompt string }

// SubmitClarificationMsg is sent when the user answers clarification questions.
type SubmitClarificationMsg struct{ Answers []ClarificationAnswer }

// ClarificationAnswer holds an answer to a clarification question.
type ClarificationAnswer struct {
	Question string
	Answer   string
}

// ApprovePlanMsg is sent when the user approves the plan.
type ApprovePlanMsg struct{}

// RejectPlanMsg is sent when the user rejects the plan.
type RejectPlanMsg struct{ Feedback string }

// EditPlanMsg is sent when the user wants to edit the plan.
type EditPlanMsg struct{ Path string }

// RetryAgentMsg is sent when the user wants to retry a failed agent.
type RetryAgentMsg struct{ AgentID string }

// SkipAgentMsg is sent when the user wants to skip a failed agent.
type SkipAgentMsg struct{ AgentID string }

// AbortRunMsg is sent when the user wants to abort.
type AbortRunMsg struct{}

// RepairWorkMsg is sent when the user wants to re-run workers with QA feedback.
type RepairWorkMsg struct{}

// AcceptQAWithWarningsMsg is sent when the user accepts despite QA warnings.
type AcceptQAWithWarningsMsg struct{}

// OrchestratorEventMsg wraps an orchestrator event for the TUI.
type OrchestratorEventMsg struct{ Event orchestrator.Event }

// GatewayResultMsg carries the gateway evaluation result.
type GatewayResultMsg struct {
	Result agent.GatewayResult
	Err    error
}

// PlanReadyMsg carries the completed plan.
type PlanReadyMsg struct {
	PlanOutput agent.PlanOutput
	Err        error
}

// RunCompleteMsg signals the orchestrator run is done.
type RunCompleteMsg struct {
	Result orchestrator.Result
	Err    error
}
