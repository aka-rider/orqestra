package orchestrator

// Phase represents the current pipeline phase.
type Phase string

const (
	PhasePlanning       Phase = "planning"
	PhaseCritiquing     Phase = "critiquing"
	PhaseExecuting      Phase = "executing"
	PhaseSelfValidating Phase = "self-validating"
)

// GateRequest is emitted when the pipeline needs user input.
type GateRequest struct {
	Position          HumanGatePosition
	FinalPlanMarkdown string
	PlanWarnings      []string
}

// DecisionType classifies user decisions at gates.
type DecisionType int

const (
	DecisionApprove DecisionType = iota
	DecisionEdit
	DecisionCancel
	DecisionComment // comment-only refinement at plan gate
)

// Decision is sent from TUI to pipeline at gates.
type Decision struct {
	Type          DecisionType
	EditedContent string
	Comment       string
	// AutoApprove treats a DecisionEdit as final approval — no re-show, no architect
	// re-engagement. Set only after user explicitly confirms edited content (^E → save → Yes).
	// Ignored when Comment is non-empty (architect re-engagement takes precedence).
	AutoApprove bool
}
