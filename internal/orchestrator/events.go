package orchestrator

import "github.com/xiii/orqestra/internal/mcp"

// EventType classifies orchestrator events emitted to the TUI.
type EventType int

const (
	EventPhaseChange EventType = iota
	EventAgentStarted
	EventAgentDone
	EventAgentFailed
	EventAgentCancelled
	EventAgentOutput
	EventAgentSkipped // emitted when a phase is disabled
	EventPlanReady
	EventGateRequest
	EventComplete
	EventError
	EventRunDirReady   // emitted once after session dir is created
	EventChatResponse  // emitted when architect answers without revising the plan
	EventUserQuestion  // emitted when an agent asks the user a question via MCP
	EventMergeConflict // emitted when the post-run merge has conflicts
	EventMergeError    // emitted when the post-run merge fails (dirty repo, permissions, etc.)
)

// Phase represents the current pipeline phase.
type Phase string

const (
	PhaseResearching    Phase = "researching"
	PhasePlanning       Phase = "planning"
	PhaseDeliberating   Phase = "deliberating"
	PhaseCritiquing     Phase = "critiquing"
	PhaseExecuting      Phase = "executing"
	PhaseSelfValidating Phase = "self-validating"
	PhaseDone           Phase = "done"
)

// GateRequest is emitted when the pipeline needs user input.
type GateRequest struct {
	Position          HumanGatePosition // which gate is firing (v6 unified layout)
	FinalPlanMarkdown string // plan markdown for review
	PlanFilePath      string // absolute path to plan.md on disk (for external editor)
	PlanWarnings      []string
}

// DecisionType classifies user decisions at gates.
type DecisionType int

const (
	DecisionApprove DecisionType = iota
	DecisionEdit
	DecisionSkip
	DecisionCancel
	DecisionComment    // comment-only refinement at plan gate
	DecisionMergeAbort // abort the post-run merge, keep the worktree branch
)

// MergeConflictInfo is carried by EventMergeConflict.
type MergeConflictInfo struct {
	WorktreeBranch string   // branch that was merged (for display)
	WorktreePath   string   // preserved worktree path for manual resolution
	TargetBranch   string   // branch that received the merge
	ConflictFiles  []string // list of conflicting files
}

// Decision is sent from TUI to pipeline at gates.
type Decision struct {
	Type          DecisionType
	EditedContent string
	Comment       string // for DecisionComment
	// AutoApprove, when true on a DecisionEdit, instructs the gate loop
	// to treat the edit as a final approval (no re-show, no architect
	// re-engagement). Set by the TUI only after the user has explicitly
	// confirmed the edited content (^E -> save -> Yes). The revert path
	// (plan-history Ctrl+Y) leaves this false so the user must re-review.
	// If Comment is non-empty, architect re-engagement takes precedence
	// and AutoApprove is ignored (user asked for another review).
	AutoApprove bool
}

// Event is emitted by the orchestrator to notify the TUI of progress.
type Event struct {
	Type        EventType
	Phase       Phase
	AgentID     string
	Gate        GateRequest
	WorkOutput  string
	OutputChunk string
	Err         error

	// New pipeline fields
	ResearchDraft    string
	FinalPlan        string
	WorkerValidation string
	Status           RunStatus // set on EventComplete
	RunDir           string    // set on EventComplete

	// Token usage from the agent's streaming events. Set on EventAgentDone.
	InputTokens  int64
	OutputTokens int64

	// Meta carries model metadata for the agent. Set on EventAgentStarted.
	Meta AgentMeta

	// ChatText is set on EventChatResponse — architect answered without revising the plan.
	ChatText string

	// UserQuestion is set on EventUserQuestion.
	UserQuestion mcp.ToolCall

	// MergeConflict is set on EventMergeConflict.
	MergeConflict MergeConflictInfo

	// MergeError is set on EventMergeError — the error message from the failed merge.
	MergeError string
	// MergeBranch is set on EventMergeError — the branch containing committed work.
	MergeBranch string
	// MergeWorktreePath is set on merge error/conflict events when manual recovery artifacts are preserved.
	MergeWorktreePath string
}
