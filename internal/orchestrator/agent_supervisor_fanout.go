package orchestrator

import "github.com/xiii/orqestra/internal/harness"

// fanoutSink forwards every event to both the real sink and the events
// channel. Observe is called from the harness sink goroutine.
//
// Split out of agent_supervisor.go (WP17 — root CLAUDE.md §1.7's 500-line
// guideline) alongside the WP17 hardening fix below.
//
// WP12/RC3: EventSessionStart capture used to require a dedicated sessionC
// channel here, feeding the supervise loop's lazy report-signal bind. Report
// correlation is now nonce-based and armed before the subprocess even starts
// (see AgentSupervisor.Run), so fanoutSink no longer needs to single out
// EventSessionStart at all — the supervise loop reads it directly off events.
//
// WP17 hardening note: EventSessionStart and EventSessionDone are delivered
// NON-lossily — everything else stays lossy (drop-if-full), same as before.
// These two kinds drive the supervise loop's capturedSID/reportSig binding
// and the graceful-stdin-close decision (AgentSupervisor.Run); silently
// dropping either one under a momentarily-full buffer would force a full
// wall-clock timeout instead of the fast paths those events unlock — a
// truthfulness regression the lossy default is fine to accept for ordinary
// stream chatter (deltas, tool calls) but not for these two.
type fanoutSink struct {
	inner  harness.Sink
	events chan<- harness.Event
	// done bounds the guaranteed-delivery sends below so they can never
	// block this sink goroutine forever: once the run's ctx is Done, the
	// supervise loop has already stopped reading events (see Run's
	// `case <-ctx.Done()` branch), so nothing would ever drain a blocked
	// send again. A bare `<-chan struct{}` (not context.Context itself) is
	// stored here — root CLAUDE.md §1.3 forbids storing a Context, not a
	// plain done-signal channel derived from one (the same pattern
	// emitter.go's forward(done <-chan struct{}) already uses).
	done <-chan struct{}
}

func (f *fanoutSink) Observe(ev harness.Event) {
	if f.inner != nil {
		f.inner.Observe(ev)
	}

	switch ev.Kind {
	case harness.EventSessionStart, harness.EventSessionDone:
		select {
		case f.events <- ev:
		case <-f.done:
		}
	default:
		select {
		case f.events <- ev:
		default: // supervisor events channel is lossy — drop if full
		}
	}
}
