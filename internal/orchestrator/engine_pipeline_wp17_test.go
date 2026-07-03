package orchestrator

import (
	"testing"

	"github.com/xiii/orqestra/internal/mcp"
)

// TestSupersedeGateDecision_NewestWinsOverStale is the WP17/F4 QA gate
// proof: engine_pipeline.go's intents-consumer must implement
// drain-then-send so the NEWEST GateDecisionIntent always occupies the
// cap-1 gateDecisions buffer — never the reverse. gate.go's own
// GateID-mismatch check (drain-before-open) only protects the NEXT gate
// from a stale decision left over from a PREVIOUS one; it does nothing to
// help THIS gate when a stale decision is already sitting in the buffer
// when a valid one for the currently-open gate arrives.
//
// RED-first proof (quoted verbatim in the WP17 report): with the
// pre-fix shape — a plain non-blocking send that drops the NEWER decision
// whenever the buffer is already full — pre-seeding a stale decision and
// then delivering a valid one for the CURRENTLY open gate leaves the STALE
// one in the buffer; this test fails because the gate would receive the
// stale (wrong-GateID) decision instead of the valid one. Drain-then-send
// (the real fix) makes it pass.
func TestSupersedeGateDecision_NewestWinsOverStale(t *testing.T) {
	gateDecisions := make(chan GateDecisionIntent, 1)

	// Pre-seed a stale decision, as if it arrived while no gate (or a
	// different, already-closed gate) was listening.
	stale := GateDecisionIntent{GateID: 1, Decision: Decision{Type: DecisionCancel}}
	gateDecisions <- stale

	// A valid decision for the CURRENTLY open gate arrives next.
	valid := GateDecisionIntent{GateID: 2, Decision: Decision{Type: DecisionApprove}}
	supersedeGateDecision(gateDecisions, valid)

	select {
	case got := <-gateDecisions:
		if got != valid {
			t.Fatalf("gateDecisions delivered %+v, want the newest decision %+v — a stale decision superseded a valid one (F4)", got, valid)
		}
	default:
		t.Fatal("gateDecisions is empty — the valid decision was dropped entirely")
	}

	// Buffer must be empty afterward (cap-1, single occupant).
	select {
	case extra := <-gateDecisions:
		t.Fatalf("expected exactly one decision in the buffer, found an extra: %+v", extra)
	default:
	}
}

// TestSupersedeGateDecision_EmptyBufferJustSends covers the common case
// (no stale decision present): the send must still land normally.
func TestSupersedeGateDecision_EmptyBufferJustSends(t *testing.T) {
	gateDecisions := make(chan GateDecisionIntent, 1)
	only := GateDecisionIntent{GateID: 5, Decision: Decision{Type: DecisionApprove}}
	supersedeGateDecision(gateDecisions, only)

	select {
	case got := <-gateDecisions:
		if got != only {
			t.Fatalf("got %+v, want %+v", got, only)
		}
	default:
		t.Fatal("expected the decision to be delivered onto an empty buffer")
	}
}

// TestDrainStaleQuestion_DropsBufferedPhantom is the WP17/F3 QA gate proof
// for the drain primitive engine_pipeline.go's question-forwarder calls
// once at startup: a question already sitting in the channel (a phantom
// left over from a previous run's forwarder that raced its own
// cancellation and lost) must be discarded, never delivered.
//
// RED-first proof (quoted verbatim in the WP17 report): with the call site
// removed (engine_pipeline.go's forwarder goroutine relaying straight into
// its main loop with no startup drain — the pre-fix shape), the SAME
// buffered ToolCall used here would instead be the FIRST thing the new
// run's forwarder relays as EventQuestionAsked onto the new run's own
// event bus — a phantom question from a dead run surfacing on the live
// one. This test targets the primitive directly (drainStaleQuestion) since
// reproducing the exact cross-run race deterministically at the full
// Engine.Start level would require sleep-based synchronization, which root
// CLAUDE.md §8 disallows; the wiring at the call site is a one-line, purely
// mechanical addition (see engine_pipeline.go) reviewed alongside this test.
func TestDrainStaleQuestion_DropsBufferedPhantom(t *testing.T) {
	questions := make(chan mcp.ToolCall, 1)
	questions <- mcp.ToolCall{ID: "q-stale", Question: "stale from a previous run"}

	drainStaleQuestion(questions)

	select {
	case q := <-questions:
		t.Fatalf("expected the buffered phantom question to be drained, got %+v", q)
	default:
	}
}

// TestDrainStaleQuestion_EmptyIsNoop covers the common case (no phantom
// present, e.g. the very first run of a session): draining an empty
// channel must be a fast, non-blocking no-op.
func TestDrainStaleQuestion_EmptyIsNoop(t *testing.T) {
	questions := make(chan mcp.ToolCall, 1)
	drainStaleQuestion(questions) // must not block or panic
}
