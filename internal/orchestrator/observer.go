package orchestrator

import "github.com/xiii/orqestra/internal/harness"

// Observer is the engine-side observation handle. Every method is non-blocking:
// callers must not rely on the observer for flow control. Lifecycle facts are
// last-write-wins; stream text is bounded and lossy; TUI reads snapshots.
type Observer interface {
	PhaseChanged(Phase)
	AgentStarted(AgentID, AgentMeta)
	AgentDone(AgentID, harness.TokenUsage)
	AgentFailed(AgentID, error)
	Stream(AgentID, harness.Event)
	GateOpened(GateRequest)
	Finished(Result, error)
}
