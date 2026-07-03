package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// closeAwareExecutor models the WP13.0 spike's CONFIRMED finding: a real
// `claude --input-format stream-json` process emits EventSessionStart then
// EventSessionDone for one turn, then sits idle waiting for more stdin input
// — until stdin is closed (the caller closes the input channel), at which
// point it exits cleanly. result records which branch fired, so tests can
// assert not just the returned error but WHETHER the input plane was closed.
type closeAwareExecutor struct {
	sessionID string
	result    string // "closed" | "got-message" | "ctx-done"
}

func (e *closeAwareExecutor) Run(ctx context.Context, _ harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	// Consume the initial prompt message (the supervisor always seeds one
	// when the input plane is open) before "doing work" — mirrors a real
	// claude process reading its first NDJSON user line off stdin.
	select {
	case <-in:
	case <-ctx.Done():
		e.result = "ctx-done"
		return harness.RunResult{SessionID: e.sessionID}, ctx.Err()
	}

	if sink != nil {
		sink.Observe(harness.Event{Kind: harness.EventSessionStart, SessionID: e.sessionID})
		sink.Observe(harness.Event{Kind: harness.EventSessionDone})
	}

	// Now mirror the spike: sit idle, waiting for either more stdin input
	// (another nudge message) or stdin to close (EOF).
	select {
	case _, ok := <-in:
		if !ok {
			e.result = "closed"
			return harness.RunResult{SessionID: e.sessionID, Output: "done"}, nil
		}
		e.result = "got-message"
		<-ctx.Done()
		return harness.RunResult{SessionID: e.sessionID}, ctx.Err()
	case <-ctx.Done():
		e.result = "ctx-done"
		return harness.RunResult{SessionID: e.sessionID}, ctx.Err()
	}
}

// neverSignaler arms a report-correlation channel that is never closed —
// models a report that never arrives.
type neverSignaler struct{}

func (neverSignaler) ReportSignal(_, _ string) <-chan struct{} {
	return make(chan struct{})
}

// TestWP13_GracefulStdinClose_NoReportExpected is the WP13.0-CONFIRMED branch
// gate (b), first half: a spec that never expects a report must have its
// input plane closed as soon as EventSessionDone fires, letting the
// subprocess exit cleanly (per the spike) instead of waiting for a timeout.
// Bounded via a short ctx so a wrong assumption (msgs never closed) is RED
// (DeadlineExceeded), not NO-VERDICT (hang).
func TestWP13_GracefulStdinClose_NoReportExpected(t *testing.T) {
	guard := NewBudgetGuard(NewRunUsage(0))
	exec := &closeAwareExecutor{sessionID: "sess-noreport"}
	sup := NewAgentSupervisor(exec, nil, guard)

	spec := harness.ProcessSpec{
		AgentID:    "worker",
		Prompt:     "do work",
		InputPlane: true,
		// ExpectsReport left false: nothing to wait for after the turn ends.
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	res, err := sup.Run(ctx, spec, nil, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected clean exit (nil error) once stdin closed, got: %v", err)
	}
	if exec.result != "closed" {
		t.Fatalf("expected the executor to observe the input channel CLOSED, got result=%q — msgs was not closed after EventSessionDone", exec.result)
	}
	if res.SessionID != "sess-noreport" {
		t.Errorf("SessionID = %q, want %q", res.SessionID, "sess-noreport")
	}
	if elapsed > 5*time.Second {
		t.Errorf("graceful close took %s — expected near-instant, well under the 10s bound", elapsed)
	}
}

// TestWP13_GracefulStdinClose_ReportExpectedButNeverArrives is the WP13.0-
// CONFIRMED branch gate (b), second half: a spec that DOES expect a report
// must NOT have its input plane closed just because one turn ended — drift/
// silence/pre-timeout nudges need the channel to stay open (owner constraint:
// "drift nudges are intentional and stay"). Bounded via spec.Timeout so a
// wrong assumption (msgs closed prematurely, or the run hanging) surfaces as
// a fast, named RED — never NO-VERDICT.
func TestWP13_GracefulStdinClose_ReportExpectedButNeverArrives(t *testing.T) {
	guard := NewBudgetGuard(NewRunUsage(0))
	exec := &closeAwareExecutor{sessionID: "sess-neverreports"}
	sup := NewAgentSupervisor(exec, neverSignaler{}, guard)

	spec := harness.ProcessSpec{
		AgentID:       "architect",
		Prompt:        "plan it",
		InputPlane:    true,
		ExpectsReport: true,
		Timeout:       800 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := sup.Run(ctx, spec, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded (spec.Timeout) since no report ever arrived, got: %v", err)
	}
	if exec.result != "ctx-done" {
		t.Fatalf("expected the executor to observe ctx.Done (never a closed input channel) — got result=%q; msgs was closed prematurely while a report was still expected", exec.result)
	}
}
