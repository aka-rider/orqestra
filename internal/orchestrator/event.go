package orchestrator

import (
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
)

// RunEvent is the single ordered event kind emitted onto a run's event bus
// (WP9/RC2 — "one pipe out"). It is a sealed union: every event the pipeline
// can produce is exactly one of the concrete types below, and a consumer
// type-switches on the concrete type rather than reading a Kind field plus a
// grab-bag of maybe-populated fields (§1.5 — mutually exclusive variants get
// ONE active sub-model, never a struct holding every variant's fields side by
// side). WP10 will type-switch over this to drive TUI state directly,
// replacing ObsSnapshot diffing.
//
// The interface is sealed to this package: runEvent() is unexported, so only
// the concrete types declared here can satisfy RunEvent. This keeps the
// union closed — a consumer's type switch can have an exhaustive default
// case without worrying about an external package inventing a new variant.
type RunEvent interface {
	// runEvent is the sealing marker. It carries no behavior.
	runEvent()
}

// EventPhaseStarted marks a pipeline phase transition (run_pipeline.go's
// sc.Obs.PhaseChanged call sites: PhasePlanning, PhaseCritiquing,
// PhaseExecuting, PhaseSelfValidating).
type EventPhaseStarted struct {
	Phase Phase
}

func (EventPhaseStarted) runEvent() {}

// EventAgentStarted marks an agent beginning a run. Always emitted before
// that agent's first EventDelta (proven by TestEmitter_OrderingAgentStartedBeforeDelta).
type EventAgentStarted struct {
	AgentID AgentID
	Meta    AgentMeta
}

func (EventAgentStarted) runEvent() {}

// EventAgentDone marks an agent's successful completion with its final,
// cumulative token usage for the run.
type EventAgentDone struct {
	AgentID AgentID
	Usage   harness.TokenUsage
}

func (EventAgentDone) runEvent() {}

// EventAgentFailed marks an agent's failure. Err is carried as a genuine
// error value (never stringified, §1.1) so a consumer can errors.Is/As it;
// call .Error() only at a display boundary.
type EventAgentFailed struct {
	AgentID AgentID
	Err     error
}

func (EventAgentFailed) runEvent() {}

// EventDelta is a streamed text chunk from an agent — either a partial
// content_block_delta or a complete non-delta text block (harness.Event has
// no separate "whole message" RunEvent kind; both map here). The emitter
// coalesces adjacent same-agent EventDelta values when the consumer lags
// (emitter.go), concatenating Text while preserving order and total content.
type EventDelta struct {
	AgentID AgentID
	Text    string
}

func (EventDelta) runEvent() {}

// EventToolCall is a tool invocation request from an agent.
type EventToolCall struct {
	AgentID AgentID
	Tool    string
	Detail  string
}

func (EventToolCall) runEvent() {}

// EventToolResult is the result of a tool invocation.
type EventToolResult struct {
	AgentID AgentID
	IsError bool
}

func (EventToolResult) runEvent() {}

// EventStats reports incremental token-usage observed mid-stream (distinct
// from EventAgentDone's final, cumulative usage).
type EventStats struct {
	AgentID AgentID
	Input   int64
	Output  int64
}

func (EventStats) runEvent() {}

// GateID identifies one gate opening/closing pair so a later intent (WP10's
// Intents channel) can be correlated to the specific gate it answers, even
// across a stale or duplicate submission. Generated once per gate opening by
// ObsStore's internal sequence counter (see ObsStore.GateOpened) — Control
// itself has no notion of gate identity, matching the plan's "keep minimal"
// guidance.
type GateID uint64

// EventGateOpened marks a human gate opening.
type EventGateOpened struct {
	GateID  GateID
	Request GateRequest
}

func (EventGateOpened) runEvent() {}

// EventGateClosed marks the matching gate's close (decision received, or ctx
// cancelled while waiting). GateID matches the EventGateOpened that opened
// it — gates are never concurrent in this pipeline (Control.Gate blocks the
// single pipeline goroutine), so "the current gate" is unambiguous.
type EventGateClosed struct {
	GateID GateID
}

func (EventGateClosed) runEvent() {}

// EventQuestionAsked marks an incoming MCP question surfaced to the user.
// ToolCall.ID (WP5) correlates the eventual answer; this bus does not carry
// the answer itself (Intents is a WP10 concern).
type EventQuestionAsked struct {
	ToolCall mcp.ToolCall
}

func (EventQuestionAsked) runEvent() {}

// EventRunFinished is the terminal event: emitted exactly once, from the same
// call that drives ObsStore.Finished (startNew's finish closure — WP2's
// single terminal writer), always LAST on the bus. The emitter closes its
// output channel immediately after delivering this event; no further Emit
// call is accepted (see emitter.go).
type EventRunFinished struct {
	Result Result
	Err    error
}

func (EventRunFinished) runEvent() {}
