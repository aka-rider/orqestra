package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// recordingSignaler records every ReportSignal/Release call — used to prove
// AgentSupervisor.Run releases the report-correlation waiter it armed,
// exactly once, after the subprocess invocation returns (WP17 bridge-hygiene
// hardening note).
type recordingSignaler struct {
	mu       sync.Mutex
	armed    []string // "agentID/nonce"
	released []string // "agentID/nonce"
}

func (r *recordingSignaler) ReportSignal(agentID, nonce string) <-chan struct{} {
	r.mu.Lock()
	r.armed = append(r.armed, agentID+"/"+nonce)
	r.mu.Unlock()
	ch := make(chan struct{})
	close(ch) // pre-fired, like preFiredSignaler — the run stops immediately
	return ch
}

func (r *recordingSignaler) Release(agentID, nonce string) {
	r.mu.Lock()
	r.released = append(r.released, agentID+"/"+nonce)
	r.mu.Unlock()
}

func (r *recordingSignaler) releasedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.released)
}

// TestSupervisor_ReleasesReportWaiterAfterRun is the WP17 bridge-hygiene QA
// gate: AgentSupervisor.Run must call Release(agentID, nonce) for every
// invocation it armed via ReportSignal, exactly once, by the time Run
// returns — otherwise the bridge's reportWaiters map grows one entry per
// invocation that never happens to be harvested at exactly the right
// moment (refactor-review-2026-07-03.md hardening note).
func TestSupervisor_ReleasesReportWaiterAfterRun(t *testing.T) {
	player := &sessionEmittingBlocker{sessionID: "test-sid"}
	guard := NewBudgetGuard(NewRunUsage(0))
	signaler := &recordingSignaler{}
	sup := NewAgentSupervisor(player, signaler, guard)

	spec := harness.ProcessSpec{
		AgentID:       "architect",
		ExpectsReport: true,
		Prompt:        "plan it",
	}

	done := make(chan error, 1)
	go func() {
		_, err := sup.Run(context.Background(), spec, nil, nil)
		done <- err
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}

	if got := signaler.releasedCount(); got != 1 {
		t.Fatalf("Release called %d times, want exactly 1 (armed=%v released=%v)", got, signaler.armed, signaler.released)
	}
	if signaler.armed[0] != signaler.released[0] {
		t.Errorf("released key %q does not match armed key %q", signaler.released[0], signaler.armed[0])
	}
}

// TestSupervisor_NoReleaseWhenReportNotExpected proves Release is only
// called for invocations that actually armed a report signal — an
// ExpectsReport:false spec (or a nil ReportSignaler) must never call
// Release on a key it never armed.
func TestSupervisor_NoReleaseWhenReportNotExpected(t *testing.T) {
	player := &blockingPlayer{player: &fixturePlayer{path: "testdata/normal_exit.jsonl"}}
	guard := NewBudgetGuard(NewRunUsage(0))
	signaler := &recordingSignaler{}
	sup := NewAgentSupervisor(player, signaler, guard)

	spec := harness.ProcessSpec{
		AgentID:       "worker",
		ExpectsReport: false,
		Timeout:       200 * time.Millisecond,
	}

	_, _ = sup.Run(context.Background(), spec, nil, nil)

	if got := signaler.releasedCount(); got != 0 {
		t.Errorf("Release called %d times for an ExpectsReport:false spec, want 0 (released=%v)", got, signaler.released)
	}
}

// TestFanoutSink_SessionStartGuaranteedDelivery_NotLossyWhenFull is the
// WP17 fanoutSink hardening QA gate: EventSessionStart and EventSessionDone
// drive the supervise loop's report-signal binding and graceful-stdin-close
// decision — losing either to a momentarily-full events buffer silently
// forces a full wall-clock timeout instead of the fast paths those events
// unlock. They must be delivered even when the buffer is full, unlike
// ordinary (lossy) event kinds.
//
// RED-first proof (quoted verbatim in the WP17 report): with a plain
// non-blocking send for every kind (the pre-fix shape), Observe returns
// immediately when the 1-slot buffer is already occupied — this test's
// "must not return yet" check fails instantly, and after room is made, the
// events channel is empty (the SessionStart was already silently dropped,
// never queued) — the final assertion ("EventSessionStart never landed")
// fails. Guaranteed delivery (the real fix) makes both pass.
func TestFanoutSink_SessionStartGuaranteedDelivery_NotLossyWhenFull(t *testing.T) {
	events := make(chan harness.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fanoutSink{events: events, done: ctx.Done()}

	// Fill the buffer with an ordinary (lossy-kind) event first.
	events <- harness.Event{Kind: harness.EventToolUse}

	observeReturned := make(chan struct{})
	go func() {
		sink.Observe(harness.Event{Kind: harness.EventSessionStart, SessionID: "sid-1"})
		close(observeReturned)
	}()

	// Sanity signal (not the crux of the proof, see below): Observe should
	// not return near-instantly while the buffer is still full and nobody
	// has drained it.
	select {
	case <-observeReturned:
		t.Fatal("Observe(EventSessionStart) returned immediately while the events buffer was full — treated as droppable instead of guaranteed-delivery")
	case <-time.After(50 * time.Millisecond):
	}

	// Drain the pre-filled event to make room for the guaranteed send.
	<-events

	select {
	case <-observeReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Observe(EventSessionStart) never completed after room was made for it")
	}

	// The crux of the proof: the event that actually landed must be the
	// SessionStart itself — if it had been dropped earlier (the lossy
	// pre-fix behavior), nothing would be here to receive.
	select {
	case got := <-events:
		if got.Kind != harness.EventSessionStart {
			t.Fatalf("events channel delivered %+v, want EventSessionStart", got)
		}
	default:
		t.Fatal("EventSessionStart never landed in the events channel — it was dropped, not guaranteed-delivered")
	}
}
