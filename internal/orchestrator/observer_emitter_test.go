package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// TestEventObserver_ZeroDeltaProseSurfaces is WP18's F5 QA gate: a stream
// with ZERO IsDelta chunks (worker_stream_sample.jsonl — prose only in
// complete "assistant" events) must still deliver its prose text through
// eventObserver.Stream -> emitter -> Events().
//
// RED-first: against the pre-fix Stream (default: return for any non-delta
// text), this test failed with "no EventDelta text collected; want text
// containing \"OK\"" — the fixture's "OK" assistant message was silently
// dropped. It passes now that non-delta text forwards when no IsDelta chunk
// preceded it in the same block.
func TestEventObserver_ZeroDeltaProseSurfaces(t *testing.T) {
	em := newEmitter(8)
	obs := newEventObserver(em)
	sink := SinkFromObserver(AgentID("worker"), obs)

	player := &fixturePlayer{path: "../harness/testdata/worker_stream_sample.jsonl"}
	if _, err := player.Run(context.Background(), harness.ProcessSpec{}, nil, sink); err != nil {
		t.Fatalf("fixturePlayer.Run: %v", err)
	}
	obs.Finished(Result{Status: StatusSuccess}, nil)

	var text strings.Builder
	for ev := range em.Events() {
		if d, ok := ev.(EventDelta); ok {
			text.WriteString(d.Text)
		}
	}

	if !strings.Contains(text.String(), "OK") {
		t.Fatalf("no EventDelta text collected; want text containing %q, got %q", "OK", text.String())
	}
}

// TestEventObserver_DeltaThenEchoRendersOnce is F5's double-render guard: a
// turn that DOES stream IsDelta chunks, followed by the CLI's non-delta
// complete-message echo of the same block, must surface that block's text
// EXACTLY ONCE on the bus — the echo must be dropped, not forwarded as a
// second EventDelta.
func TestEventObserver_DeltaThenEchoRendersOnce(t *testing.T) {
	em := newEmitter(8)
	obs := newEventObserver(em)
	const agentID = AgentID("worker")

	obs.AgentStarted(agentID, AgentMeta{ModelRef: "test-model"})
	obs.Stream(agentID, harness.Event{Kind: harness.EventChunk, Text: "Hello, ", IsDelta: true})
	obs.Stream(agentID, harness.Event{Kind: harness.EventChunk, Text: "world!", IsDelta: true})
	// The CLI's complete-message echo of the same block, non-delta.
	obs.Stream(agentID, harness.Event{Kind: harness.EventChunk, Text: "Hello, world!", IsDelta: false})
	obs.Finished(Result{Status: StatusSuccess}, nil)

	var deltas []string
	for ev := range em.Events() {
		if d, ok := ev.(EventDelta); ok {
			deltas = append(deltas, d.Text)
		}
	}

	full := strings.Join(deltas, "")
	if got := strings.Count(full, "Hello, world!"); got != 1 {
		t.Fatalf("want \"Hello, world!\" exactly once across %d EventDelta event(s) (%q), got %d", len(deltas), deltas, got)
	}
}
