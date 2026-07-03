package orchestrator

import "github.com/xiii/orqestra/internal/harness"

// eventObserver is the WP10/RC2 Observer implementation: every method emits
// the equivalent RunEvent directly onto the run's emitter — there is no
// intervening snapshot store (the pre-WP10 store is deleted). This replaces
// its dual role (snapshot store + emitter tee) with a single, small
// adapter; the emitter itself already guarantees lifecycle events are never
// dropped and deltas coalesce under a slow consumer (emitter.go).
type eventObserver struct {
	em *emitter
}

// newEventObserver returns an Observer backed by em.
func newEventObserver(em *emitter) *eventObserver {
	return &eventObserver{em: em}
}

func (o *eventObserver) PhaseChanged(p Phase) {
	o.em.Emit(EventPhaseStarted{Phase: p})
}

func (o *eventObserver) AgentStarted(id AgentID, meta AgentMeta) {
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
// Deliberate omission: a non-delta "assistant" message event (ev.Text != ""
// && !ev.IsDelta) is the CLAUDE CLI's complete-message echo of a text block
// already streamed chunk-by-chunk via preceding IsDelta events (see
// harness/stream_event.go — the "assistant" case joins the same content the
// prior "content_block_delta" events already delivered). The emitter
// coalesces adjacent same-agent EventDelta values by CONCATENATING Text
// (emitter.go, tested by TestEmitter_SlowConsumerNeverDropsLifecycleAndPreservesDeltaText) —
// forwarding the duplicate complete-message text as another EventDelta would
// double-render the turn's prose. Every other harness.Event kind maps 1:1.
func (o *eventObserver) Stream(id AgentID, ev harness.Event) {
	var busEvent RunEvent
	switch {
	case ev.IsDelta:
		busEvent = EventDelta{AgentID: id, Text: ev.Text}
	case ev.Tool != "":
		busEvent = EventToolCall{AgentID: id, Tool: ev.Tool, Detail: ev.Detail}
	case ev.Kind == harness.EventToolResult:
		busEvent = EventToolResult{AgentID: id, IsError: ev.IsError}
	case ev.Kind == harness.EventUsage:
		busEvent = EventStats{AgentID: id, Input: ev.Input, Output: ev.Output}
	default:
		return
	}
	o.em.Emit(busEvent)
}

func (o *eventObserver) Finished(res Result, err error) {
	// EventRunFinished is the terminal, always-last event on the bus (WP2's
	// single terminal writer — Finished is called exactly once per run, from
	// startNew's finish closure).
	o.em.Emit(EventRunFinished{Result: res, Err: err})
}
