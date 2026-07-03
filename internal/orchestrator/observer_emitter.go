package orchestrator

import (
	"sync"

	"github.com/xiii/orqestra/internal/harness"
)

// eventObserver is the WP10/RC2 Observer implementation: every method emits
// the equivalent RunEvent directly onto the run's emitter — there is no
// intervening snapshot store (the pre-WP10 store is deleted). This replaces
// its dual role (snapshot store + emitter tee) with a single, small
// adapter; the emitter itself already guarantees lifecycle events are never
// dropped and deltas coalesce under a slow consumer (emitter.go).
type eventObserver struct {
	em *emitter

	// mu guards sawDelta (WP18/F5). In this pipeline Stream is invoked only
	// from the ONE harness sink goroutine active at a time — exec.go joins
	// that goroutine (<-sinkDone) before Run returns, and run_pipeline.go
	// never starts a second agent invocation before the previous one
	// returns — so sequential access is already guaranteed without a lock.
	// The mutex is kept anyway because that invariant lives in a different
	// package and file (harness/exec.go + orchestrator/step_*.go) than this
	// one; a future change on either side (e.g. concurrent sub-agents)
	// could silently violate it, and eventObserver's own contract (Observer
	// methods are "non-blocking", observer.go:6) is cheap to keep genuinely
	// safe with a plain map lock rather than resting on a cross-package
	// sequencing argument.
	mu       sync.Mutex
	sawDelta map[AgentID]bool
}

// newEventObserver returns an Observer backed by em.
func newEventObserver(em *emitter) *eventObserver {
	return &eventObserver{em: em, sawDelta: make(map[AgentID]bool)}
}

func (o *eventObserver) PhaseChanged(p Phase) {
	o.em.Emit(EventPhaseStarted{Phase: p})
}

func (o *eventObserver) AgentStarted(id AgentID, meta AgentMeta) {
	// A fresh invocation starts a fresh block: clear any leftover "saw a
	// delta" flag from a prior invocation of the same AgentID (e.g. a retry
	// whose stream ended mid-block, before its complete-message echo
	// arrived) so this invocation's own zero-delta text isn't wrongly
	// treated as an echo of the previous one's.
	o.mu.Lock()
	o.sawDelta[id] = false
	o.mu.Unlock()
	o.em.Emit(EventAgentStarted{AgentID: id, Meta: meta})
}

func (o *eventObserver) AgentDone(id AgentID, usage harness.TokenUsage) {
	o.em.Emit(EventAgentDone{AgentID: id, Usage: usage})
}

func (o *eventObserver) AgentFailed(id AgentID, err error) {
	o.em.Emit(EventAgentFailed{AgentID: id, Err: err})
}

// Stream converts one harness.Event into its RunEvent equivalent.
//
// F5 fix (was: deliberate omission dropping all non-delta text — a
// regression from pre-refactor EntryText's fallback): a non-delta text event
// (ev.Kind == EventChunk, ev.Text != "", !ev.IsDelta) is EITHER (a) the
// CLAUDE CLI's complete-message echo of a text block already streamed
// chunk-by-chunk via preceding IsDelta events (see harness/stream_event.go —
// the "assistant" case joins the same content the prior
// "content_block_delta" events already delivered), OR (b) — when the CLI
// never streamed deltas for this block at all (a zero-delta turn, e.g.
// testdata/worker_stream_sample.jsonl; or a non-JSON diagnostic line,
// stream_event.go's parseStreamLines) — the ONLY carrier of that text. Case
// (a) must still be dropped (the emitter coalesces adjacent same-agent
// EventDelta values by CONCATENATING Text — emitter.go, tested by
// TestEmitter_SlowConsumerNeverDropsLifecycleAndPreservesDeltaText —
// forwarding the duplicate complete-message text as another EventDelta would
// double-render the turn's prose); case (b) must be forwarded or the text is
// lost entirely. sawDelta (per-AgentID, reset by AgentStarted and after every
// non-delta text event here) distinguishes them: forward case (b), drop case
// (a). Every other harness.Event kind maps 1:1.
func (o *eventObserver) Stream(id AgentID, ev harness.Event) {
	var busEvent RunEvent
	switch {
	case ev.IsDelta:
		o.mu.Lock()
		o.sawDelta[id] = true
		o.mu.Unlock()
		busEvent = EventDelta{AgentID: id, Text: ev.Text}
	case ev.Tool != "":
		busEvent = EventToolCall{AgentID: id, Tool: ev.Tool, Detail: ev.Detail}
	case ev.Kind == harness.EventToolResult:
		busEvent = EventToolResult{AgentID: id, IsError: ev.IsError}
	case ev.Kind == harness.EventUsage:
		busEvent = EventStats{AgentID: id, Input: ev.Input, Output: ev.Output}
	case ev.Kind == harness.EventChunk && ev.Text != "":
		o.mu.Lock()
		hadDelta := o.sawDelta[id]
		o.sawDelta[id] = false // this block is over either way
		o.mu.Unlock()
		if hadDelta {
			return // case (a): already rendered via the preceding EventDelta chunks
		}
		busEvent = EventDelta{AgentID: id, Text: ev.Text} // case (b): only chance to see this text
	default:
		return
	}
	o.em.Emit(busEvent)
}

// ReportHarvested surfaces which tier produced a report-producing step's
// deliverable (WP11 provenance) as a bus event.
func (o *eventObserver) ReportHarvested(id AgentID, prov ReportProvenance) {
	o.em.Emit(EventReportHarvested{AgentID: id, Provenance: prov})
}

func (o *eventObserver) Finished(res Result, err error) {
	// EventRunFinished is the terminal, always-last event on the bus (WP2's
	// single terminal writer — Finished is called exactly once per run, from
	// startNew's finish closure).
	o.em.Emit(EventRunFinished{Result: res, Err: err})
}
