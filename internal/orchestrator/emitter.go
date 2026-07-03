package orchestrator

import (
	"context"
	"sync"
)

// emitter is the single-producer-side forwarder that turns Observer-driven
// pipeline calls into one ordered RunEvent stream (WP9/RC2 — "one pipe
// out"). Contract:
//
//   - LIFECYCLE events (every RunEvent except EventDelta) are NEVER dropped.
//     Emit never blocks the pipeline: every call appends to an internal,
//     mutex-protected queue and returns immediately; a single internal
//     forwarding goroutine drains that queue onto the buffered, exported
//     Events() channel. A slow or entirely absent consumer therefore cannot
//     stall the producer — worst case the internal queue keeps growing for
//     the lifetime of one run (accumulate-not-drop policy; see "No-consumer
//     policy" below).
//   - EventDelta is coalesced: if the event being emitted is an EventDelta
//     for the same AgentID as the queue's current tail element (itself an
//     EventDelta), the two are merged into one (concatenated Text) instead
//     of growing the queue. Any other event appended in between — a
//     lifecycle event, or a Delta for a different agent — ends that run, so
//     at most one pending merged Delta per agent can exist between any two
//     lifecycle events.
//   - Ordering is FIFO: events are delivered on Events() in the exact order
//     Emit was called (coalesced deltas keep the position of the first of
//     the run they merged into).
//   - Events() is closed exactly once, immediately after an EventRunFinished
//     has been sent OR — WP17/A2 — the run's ctx (passed to newEmitter) is
//     Done while forward() is genuinely BLOCKED trying to send some earlier,
//     already-queued event to a consumer that stopped draining (the
//     cross-run-bleed scenario, F1/A3: the TUI moved on to a new run and
//     will never read this one's Events() again). This is deliberately
//     narrow: ctx-Done is only ever consulted from inside the SECOND,
//     buffer-full-only select below — never while the queue is merely idle
//     (empty, waiting for the next event). A run's own ctx is cancelled by
//     startNew's runCancel() as literally the first step of every run's
//     finish() — well BEFORE EventRunFinished is even Emitted — so if an
//     idle wait were also abandonment-aware, it would race ahead of (and
//     usually win against) a real, still-attached consumer's terminal
//     event on every ordinary run; this was caught and reverted during WP17
//     development (see the emitter_test.go RED/GREEN pair, and the
//     regression it originally caused in TestEngineStart_* before the
//     narrower fix). Gating on actual buffer fullness means the escape hatch
//     only ever fires when there is truly a backlog nobody is draining.
//   - No time.Sleep anywhere: the forwarder blocks only on a channel send or
//     a sync.Cond wait; both are released by Emit/queue activity or ctx
//     cancellation, never by a fixed delay (root CLAUDE.md §8).
//
// No-consumer policy (WP9 step 4): if nobody ever reads Events(), the
// forwarding goroutine fills the buffered output channel and then blocks
// trying to send the next event — but that block is confined to the
// forwarder's own goroutine, never to the caller of Emit (the pipeline). A
// run therefore always completes without blocking even when Events is never
// drained. WP17/A2 refines the *forwarder's own* fate once the run's ctx is
// Done: instead of blocking forever on that stalled send, it gives up and
// closes Events() — see forward() below.
type emitter struct {
	mu       sync.Mutex
	cond     *sync.Cond
	queue    []RunEvent
	terminal bool // true once EventRunFinished has been queued; Emit becomes a no-op after
	out      chan RunEvent

	// forwardDone is closed once forward() has returned, on ANY exit path
	// (EventRunFinished delivered, or a stalled send abandoned via ctx).
	// Unexported: white-box test instrumentation only, proving the "forward
	// never leaks past an abandoned run" invariant (WP17/A2 QA gate)
	// without an exported API.
	forwardDone chan struct{}
}

// newEmitter creates an emitter and starts its forwarding goroutine, bound
// to ctx's lifetime (WP17/A2 — pass the RUN's own ctx, e.g.
// engine_pipeline.go's runCtx). bufSize sizes only the OUTPUT channel (a
// convenience for a fast, attentive consumer); it never bounds the internal
// queue and never applies backpressure to Emit.
func newEmitter(ctx context.Context, bufSize int) *emitter {
	e := &emitter{
		out:         make(chan RunEvent, bufSize),
		forwardDone: make(chan struct{}),
	}
	e.cond = sync.NewCond(&e.mu)
	go e.forward(ctx.Done())
	return e
}

// Events returns the single ordered consumer channel. Closed exactly once —
// see the type doc for the two terminal conditions (RunFinished delivered,
// or a stalled send abandoned via ctx).
func (e *emitter) Events() <-chan RunEvent { return e.out }

// Emit enqueues ev for delivery and returns immediately — it never blocks on
// the consumer. A Delta event may be merged into the queue's current tail
// instead of appended (see the coalescing rule in the type doc). Emit is a
// no-op once an EventRunFinished has already been queued (the run is over;
// accepting more events after that would violate "RunFinished is always
// last").
func (e *emitter) Emit(ev RunEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.terminal {
		return // fire-and-forget: Emit after RunFinished is a caller contract violation, not a run failure — RunFinished has already been queued/delivered and the bus is closing
	}

	if d, ok := ev.(EventDelta); ok && len(e.queue) > 0 {
		if last, ok2 := e.queue[len(e.queue)-1].(EventDelta); ok2 && last.AgentID == d.AgentID {
			e.queue[len(e.queue)-1] = EventDelta{AgentID: d.AgentID, Text: last.Text + d.Text}
			e.cond.Signal()
			return
		}
	}

	e.queue = append(e.queue, ev)
	if _, ok := ev.(EventRunFinished); ok {
		e.terminal = true
	}
	e.cond.Signal()
}

// forward drains the internal queue in FIFO order onto Events(), blocking
// only on the sync.Cond (when the queue is empty — this wait is NEVER
// abandonment-aware, see the type doc for why) or the output channel send
// (when the consumer lags). A send always tries a plain non-blocking path
// first: when the output buffer has room — the overwhelming common case for
// a live, attentive consumer — that is the ONLY case considered, so no race
// with ctx is even possible (a select with one communicating case plus
// `default` is deterministic). Only when the buffer is genuinely full does
// forward fall back to a real blocking select that also watches ctx.Done(),
// so a forwarder stuck behind a consumer that will never come back (WP17/A2)
// can still exit rather than leak forever. It exits, closing Events(),
// immediately after delivering EventRunFinished, or the moment that
// abandonment escape fires.
func (e *emitter) forward(done <-chan struct{}) {
	defer close(e.forwardDone)
	for {
		e.mu.Lock()
		for len(e.queue) == 0 {
			e.cond.Wait()
		}
		ev := e.queue[0]
		if len(e.queue) == 1 {
			e.queue = nil // release the backing array once fully drained
		} else {
			e.queue = e.queue[1:]
		}
		e.mu.Unlock()

		select {
		case e.out <- ev:
		default:
			select {
			case e.out <- ev:
			case <-done:
				close(e.out)
				return
			}
		}

		if _, ok := ev.(EventRunFinished); ok {
			close(e.out)
			return
		}
	}
}
