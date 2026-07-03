package orchestrator

import "github.com/xiii/orqestra/internal/harness"

// Observer is the engine-side observation handle. Every method is
// non-blocking: callers must not rely on the observer for flow control.
// WP10/RC2: the TUI no longer reads a diffed snapshot of this state — the
// concrete implementation (eventObserver, observer_emitter.go) emits the
// corresponding RunEvent onto the WP9 event bus for every call below. Gate
// lifecycle (open/close) is NOT part of Observer — it is handled directly by
// the gate mechanism (gate.go), which has its own access to the emitter and
// needs no snapshot-store intermediary.
type Observer interface {
	PhaseChanged(Phase)
	AgentStarted(AgentID, AgentMeta)
	AgentDone(AgentID, harness.TokenUsage)
	AgentFailed(AgentID, error)
	Stream(AgentID, harness.Event)
	Finished(Result, error)
	// ReportHarvested records which tier supplied a report-producing step's
	// deliverable (WP11/RC3) — a scavenge is fine, a SILENT scavenge is not.
	ReportHarvested(AgentID, ReportProvenance)
}
