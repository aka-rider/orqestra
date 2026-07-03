package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
)

// HumanGatePosition marks where in the pipeline a human gate fires.
type HumanGatePosition int

const (
	GateAfterDeliberation HumanGatePosition = iota
)

// IsPlanGate reports whether this gate position requires plan review (rich gate).
func (p HumanGatePosition) IsPlanGate() bool { return p == GateAfterDeliberation }

// String returns a human-readable name for the gate position.
func (p HumanGatePosition) String() string {
	switch p {
	case GateAfterDeliberation:
		return "after deliberation"
	default:
		return fmt.Sprintf("gate position %d", int(p))
	}
}

// HumanGateSet is an ordered list of active gate positions.
type HumanGateSet []HumanGatePosition

// Active reports whether pos is in the set.
func (h HumanGateSet) Active(pos HumanGatePosition) bool {
	for _, p := range h {
		if p == pos {
			return true
		}
	}
	return false
}

// Toggle returns a new set with pos added if absent, or removed if present.
func (h HumanGateSet) Toggle(pos HumanGatePosition) HumanGateSet {
	for i, p := range h {
		if p == pos {
			out := make(HumanGateSet, 0, len(h)-1)
			out = append(out, h[:i]...)
			return append(out, h[i+1:]...)
		}
	}
	return append(append(HumanGateSet(nil), h...), pos)
}

// GateFunc publishes a gate request and blocks until a decision arrives or
// ctx is done. It replaces the pre-WP10 Control interface: StepContext.Gate
// (step.go) is the pipeline's sole gate mechanism now.
type GateFunc func(ctx context.Context, req GateRequest) (Decision, error)

// newGateFunc returns a GateFunc bound to em (for EventGateOpened/
// EventGateClosed lifecycle events) and decisions (the run's internal,
// forwarded-from-Intents channel of GateDecisionIntent values — see
// engine_pipeline.go's intents consumer). Gates in this pipeline are never
// concurrent — RunPipeline's gate loop blocks the single pipeline goroutine —
// so the unsynchronized sequence counter closed over by the returned func is
// safe. GateID generation lives here now, replacing the pre-WP10 snapshot
// store's own gate sequence counter (WP10/RC2).
func newGateFunc(em *emitter, decisions <-chan GateDecisionIntent) GateFunc {
	var seq uint64
	return func(ctx context.Context, req GateRequest) (Decision, error) {
		// Drain any stale buffered decision before opening (WP4a/J2): a
		// decision delivered while no gate was open (a double submit, or a
		// race with the previous gate's close) must never silently satisfy
		// the NEXT gate before the user has even seen it.
		select {
		case <-decisions:
		default:
		}

		seq++
		gid := GateID(seq)
		em.Emit(EventGateOpened{GateID: gid, Request: req})
		defer em.Emit(EventGateClosed{GateID: gid})

		for {
			select {
			case dec, ok := <-decisions:
				if !ok {
					return Decision{}, fmt.Errorf("gate: intents channel closed")
				}
				if dec.GateID != gid {
					// Mismatched IDs are drained and dropped, never applied to
					// this gate (WP4a/J2 invariant, preserved by construction).
					slog.Debug("gate: dropped decision for a different gate", "want", gid, "got", dec.GateID)
					continue
				}
				return dec.Decision, nil
			case <-ctx.Done():
				return Decision{}, fmt.Errorf("gate: %w", ctx.Err())
			}
		}
	}
}
