package orchestrator

import "sync"

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
//     has been sent — the forwarding goroutine sends RunFinished, closes the
//     channel, then exits. No separate Close call exists or is needed.
//   - No time.Sleep anywhere: the forwarder blocks only on a channel send or
//     a sync.Cond wait; both are released by Emit/queue activity, never by a
//     fixed delay (root CLAUDE.md §8).
//
// No-consumer policy (WP9 step 4): if nobody ever reads Events(), the
// forwarding goroutine fills the buffered output channel and then blocks
// trying to send the next event — but that block is confined to the
// forwarder's own goroutine, never to the caller of Emit (the pipeline). A
// run therefore always completes without blocking even when Events is never
// drained; the cost is a leaked, permanently-blocked forwarder goroutine
// (and its already-queued backlog) for that one run — an accepted tradeoff
// until WP10 wires a real consumer. This is the "accumulate" policy named in
// plan-simplify-architecture.md WP9 step 4, chosen over "drop when no
// consumer was ever attached" because there is no reliable, race-free way
// for the emitter to know in advance that a consumer will never attach
// (RunHandle.Events is exposed unconditionally, per step 4's "may be nil
// when unused" being the ALTERNATIVE this design does not need: it always
// hands back a real, usable channel).
type emitter struct {
	mu       sync.Mutex
	cond     *sync.Cond
	queue    []RunEvent
	terminal bool // true once EventRunFinished has been queued; Emit becomes a no-op after
	out      chan RunEvent
}

// newEmitter creates an emitter and starts its forwarding goroutine. bufSize
// sizes only the OUTPUT channel (a convenience for a fast, attentive
// consumer); it never bounds the internal queue and never applies
// backpressure to Emit.
func newEmitter(bufSize int) *emitter {
	e := &emitter{
		out: make(chan RunEvent, bufSize),
	}
	e.cond = sync.NewCond(&e.mu)
	go e.forward()
	return e
}

// Events returns the single ordered consumer channel. Closed exactly once,
// always after an EventRunFinished has been sent.
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
// only on the sync.Cond (when the queue is empty) or the output channel send
// (when the consumer lags) — never on a timer. It exits, closing Events(),
// immediately after delivering EventRunFinished.
func (e *emitter) forward() {
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

		e.out <- ev // may block here on a slow/absent consumer — confined to this goroutine, never the producer

		if _, ok := ev.(EventRunFinished); ok {
			close(e.out)
			return
		}
	}
}
