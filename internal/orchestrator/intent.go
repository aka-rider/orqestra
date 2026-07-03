package orchestrator

import "github.com/xiii/orqestra/internal/mcp"

// Intent is the single ordered "one pipe in" for a run (WP10/RC1): every
// decision a caller (the TUI, or a future headless driver) sends back to a
// running pipeline is exactly one of the concrete types below. Sealed the
// same way RunEvent is (event.go): intent() is unexported, so only the
// concrete types declared here satisfy Intent, and a consumer's type switch
// can have an exhaustive default case without worrying about an external
// package inventing a new variant (§1.5 — one active sub-model, not parallel
// fields + bools).
type Intent interface {
	// intent is the sealing marker. It carries no behavior.
	intent()
}

// GateDecisionIntent answers an open human gate. GateID must match the
// EventGateOpened.GateID the decision answers; the gate mechanism (gate.go)
// drops a mismatched or stale GateID (e.g. a double-submit, or a decision
// that arrived before any gate opened) rather than misapplying it to a
// different or later gate — preserving the WP4a/J2 invariant by
// construction.
type GateDecisionIntent struct {
	GateID   GateID
	Decision Decision
}

func (GateDecisionIntent) intent() {}

// QuestionAnswerIntent answers a pending MCP AskUserQuestion. QuestionID
// identifies the question this answers (equal to Answer.ID by convention);
// routing (startNew's per-run intents consumer) forwards Answer to
// QuestionBridge.SendAnswer, which independently validates the ID against
// its own pending question (WP5/J17,J25) — so a stale or mismatched answer
// can never satisfy the wrong question even if a caller got QuestionID and
// Answer.ID out of sync.
type QuestionAnswerIntent struct {
	QuestionID string
	Answer     mcp.Answer
}

func (QuestionAnswerIntent) intent() {}
