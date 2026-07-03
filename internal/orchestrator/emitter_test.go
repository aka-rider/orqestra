package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/rundir"
)

// TestEmitter_SlowConsumerNeverDropsLifecycleAndPreservesDeltaText is WP9 QA
// gate (a): a producer emits 10k Deltas for one agent interleaved with
// lifecycle events while the consumer is maximally lagged — it does not
// start draining Events() until every Emit call (including the terminal
// EventRunFinished) has already returned. This is a channel/queue-driven
// "gated consumer" (no time.Sleep anywhere): the gate is simply "don't read
// until told", which the emitter's own internal queue must absorb without
// ever discarding a lifecycle event, and delta TEXT must be fully preserved
// in order even though many adjacent same-agent deltas coalesce into fewer
// events.
//
// RED-first proof (quoted verbatim in the WP9 report): this test was run
// against a deliberately-broken emitter.Emit that drops a lifecycle event
// once the internal queue backs up past a small threshold (simulating "make
// lifecycle sends non-blocking drops") and failed with a lifecycle-count
// mismatch; reverting the break made it pass again.
func TestEmitter_SlowConsumerNeverDropsLifecycleAndPreservesDeltaText(t *testing.T) {
	em := newEmitter(4) // deliberately small output buffer: the queue must do the work

	const agentID = AgentID("worker")
	const numDeltas = 10000

	var wantText strings.Builder
	lifecycleWant := 0

	em.Emit(EventAgentStarted{AgentID: agentID, Meta: AgentMeta{ModelRef: "test-model"}})
	lifecycleWant++

	for i := 0; i < numDeltas; i++ {
		chunk := fmt.Sprintf("chunk-%d ", i)
		em.Emit(EventDelta{AgentID: agentID, Text: chunk})
		wantText.WriteString(chunk)

		if i%500 == 499 {
			em.Emit(EventToolCall{AgentID: agentID, Tool: "Bash", Detail: fmt.Sprintf("call-%d", i)})
			lifecycleWant++
			em.Emit(EventStats{AgentID: agentID, Input: int64(i), Output: int64(i * 2)})
			lifecycleWant++
		}
	}

	em.Emit(EventAgentDone{AgentID: agentID, Usage: harness.TokenUsage{Input: 1, Output: 2}})
	lifecycleWant++
	em.Emit(EventRunFinished{Result: Result{Status: StatusSuccess}})
	lifecycleWant++

	// Only now — after every Emit above has already returned — start
	// draining. Nothing here is timing-dependent: the queue already holds
	// (or the forwarder has already partially flushed) the full production.
	var got []RunEvent
	for ev := range em.Events() {
		got = append(got, ev)
	}

	var gotText strings.Builder
	gotLifecycle := 0
	for _, ev := range got {
		if d, ok := ev.(EventDelta); ok {
			if d.AgentID != agentID {
				t.Fatalf("delta for unexpected agent %q", d.AgentID)
			}
			gotText.WriteString(d.Text)
			continue
		}
		gotLifecycle++
	}

	if gotText.String() != wantText.String() {
		t.Fatalf("delta text mismatch after coalescing: got %d bytes, want %d bytes",
			gotText.Len(), wantText.Len())
	}
	if gotLifecycle != lifecycleWant {
		t.Fatalf("lifecycle event count = %d, want %d — a slow consumer must never lose a lifecycle event",
			gotLifecycle, lifecycleWant)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one event")
	}
	if _, ok := got[len(got)-1].(EventRunFinished); !ok {
		t.Fatalf("last event = %T, want EventRunFinished", got[len(got)-1])
	}
}

// TestEmitter_OrderingAgentStartedPrecedesDeltaAndRunFinishedIsLast is WP9 QA
// gate (b): AgentStarted must always precede that agent's first Delta, and
// RunFinished must always be the last event delivered, with Events() closing
// immediately after.
//
// RED-first proof (quoted verbatim in the WP9 report): this test was run
// against a deliberately-broken emitter.forward that pops the queue's TAIL
// (LIFO) instead of its head (FIFO) — RunFinished (queued last) was
// delivered FIRST — and failed; reverting the break made it pass again.
func TestEmitter_OrderingAgentStartedPrecedesDeltaAndRunFinishedIsLast(t *testing.T) {
	em := newEmitter(1)

	em.Emit(EventAgentStarted{AgentID: "worker", Meta: AgentMeta{}})
	em.Emit(EventDelta{AgentID: "worker", Text: "partial"})
	em.Emit(EventToolCall{AgentID: "worker", Tool: "Bash"})
	em.Emit(EventAgentDone{AgentID: "worker", Usage: harness.TokenUsage{}})
	em.Emit(EventRunFinished{Result: Result{Status: StatusSuccess}})

	var got []RunEvent
	for ev := range em.Events() {
		got = append(got, ev)
	}

	startedIdx, deltaIdx := -1, -1
	for i, ev := range got {
		switch ev.(type) {
		case EventAgentStarted:
			if startedIdx == -1 {
				startedIdx = i
			}
		case EventDelta:
			if deltaIdx == -1 {
				deltaIdx = i
			}
		}
	}
	if startedIdx == -1 || deltaIdx == -1 {
		t.Fatalf("expected both AgentStarted and Delta events among %d events", len(got))
	}
	if startedIdx >= deltaIdx {
		t.Fatalf("AgentStarted (idx %d) must precede the agent's first Delta (idx %d)", startedIdx, deltaIdx)
	}

	if len(got) == 0 {
		t.Fatal("expected at least one event")
	}
	if _, ok := got[len(got)-1].(EventRunFinished); !ok {
		t.Fatalf("last event = %T, want EventRunFinished (RunFinished must always be last)", got[len(got)-1])
	}

	// range above only terminates on channel close, which already proves
	// this; assert explicitly too for a clearer failure message.
	if _, ok := <-em.Events(); ok {
		t.Fatal("Events() produced a value after RunFinished — the channel should already be closed")
	}
}

// TestEngineStart_NoEventConsumer_DoesNotBlockPipeline is WP9 QA gate (c): a
// real Engine.Start run must reach a terminal state even when RunHandle.Events
// is never drained during the run. A nonexistent architect binary makes the
// run fail fast (StatusFailed) without needing a real claude CLI, while still
// exercising the full startNew goroutine and its emitter wiring.
//
// RED-first proof (quoted verbatim in the WP9 report): this test was run
// against a deliberately-broken emitter.Emit that sends directly and
// synchronously to the output channel, bypassing the internal queue and
// forwarder entirely, combined with a zero-sized output buffer — the
// pipeline goroutine deadlocked on its very first lifecycle Emit call (no
// reader present during the run) and the test timed out waiting for the run
// to finish; reverting both changes made it pass again. (A weaker variant —
// bypassing only the queue, keeping the real 64-slot buffer — did not block
// the run itself, since this test's event volume fits in 64 slots, but it
// broke the "close exactly once after RunFinished" contract instead: the
// post-run drain hung forever because closing Events() only ever happens
// inside the forwarder, which the bypass never feeds. Both are genuine
// WP9-relevant regressions; the stronger variant is the one restored here.)
func TestEngineStart_NoEventConsumer_DoesNotBlockPipeline(t *testing.T) {
	cfg := config.DefaultConfig()
	engine := &Engine{
		Config: cfg,
		RunDirFactory: func(slug string) (rundir.Dir, error) {
			return rundir.Dir{Path: t.TempDir()}, nil
		},
		Specs: ProcessSpecs{
			// A nonexistent binary makes harness.Run fail immediately without a
			// real claude CLI, while still exercising the whole startNew path
			// (including every Observer call the emitter is wired behind).
			Architect: harness.ProcessSpec{Binary: "/nonexistent-orqestra-test-binary-wp9"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	handle := engine.Start(ctx, Input{
		Prompt:     "test",
		Setup:      PipelineSetup{Execution: false, Validation: false, DeliberationRounds: 1},
		SetupValid: true,
	})

	if handle.Events == nil {
		t.Fatal("RunHandle.Events is nil — WP9 must always wire a real event bus")
	}

	// Deliberately do NOT read from handle.Events while the run is in
	// flight — the adversarial "no consumer" condition. The pipeline must
	// still reach a terminal state; runDone is closed independently of
	// whether Events is ever drained (white-box test instrumentation, see
	// RunHandle.runDone's doc comment).
	select {
	case <-handle.runDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the run to finish — an undrained event bus must never block the pipeline")
	}

	// Now prove the bus actually accumulated everything (the "accumulate,
	// don't drop" no-consumer policy) rather than silently discarding it, by
	// draining it after the fact.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()
	sawRunFinished := false
	for {
		select {
		case ev, ok := <-handle.Events:
			if !ok {
				if !sawRunFinished {
					t.Fatal("event bus closed without ever delivering EventRunFinished")
				}
				return
			}
			if _, ok := ev.(EventRunFinished); ok {
				sawRunFinished = true
			}
		case <-drainCtx.Done():
			t.Fatal("timed out draining the event bus after the run completed")
		}
	}
}
