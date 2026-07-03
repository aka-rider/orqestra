package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// recordingObserver is a minimal Observer test double for tests that need
// only "some Observer" (e.g. to satisfy StepContext) plus the ability to
// assert AgentFailed was called for a given agent ID. It replaces the
// pre-WP10 pattern of using a real ObsStore + obs.Snapshot() purely as a
// generic Observer fixture.
type recordingObserver struct {
	mu     sync.Mutex
	failed map[string]bool
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{failed: make(map[string]bool)}
}

func (r *recordingObserver) PhaseChanged(Phase)             {}
func (r *recordingObserver) AgentStarted(AgentID, AgentMeta) {}
func (r *recordingObserver) AgentDone(AgentID, harness.TokenUsage) {}
func (r *recordingObserver) AgentFailed(id AgentID, _ error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed[string(id)] = true
}
func (r *recordingObserver) Stream(AgentID, harness.Event) {}
func (r *recordingObserver) ReportHarvested(AgentID, ReportProvenance) {}
func (r *recordingObserver) Finished(Result, error)        {}

// Failed reports whether AgentFailed was ever called for id.
func (r *recordingObserver) Failed(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed[id]
}

// newGateTestContext builds a StepContext wired to a fresh emitter/gate pair
// for tests that drive RunPipeline's human-gate loop directly — replacing
// the pre-WP10 ObsStore+Control fixture. The returned events channel lets a
// test driver watch for EventGateOpened/EventPhaseStarted/etc.; the returned
// decisions channel is what a driver sends GateDecisionIntent values on
// (mirroring the real per-run intents consumer in engine_pipeline.go,
// simplified to a direct channel since these tests call RunPipeline
// directly, not through Engine.Start).
func newGateTestContext() (sc StepContext, events <-chan RunEvent, decisions chan<- GateDecisionIntent) {
	em := newEmitter(256)
	decisionsCh := make(chan GateDecisionIntent, 1)
	sc = StepContext{
		Exec:      nil,
		Obs:       newEventObserver(em),
		Artifacts: NoopArtifactSink(),
		Gate:      newGateFunc(em, decisionsCh),
		Log:       slog.Default(),
	}
	return sc, em.Events(), decisionsCh
}

// noopStepContext returns a StepContext with nil Executor — engine routing
// tests don't call sc.Exec.Run(); nil makes any accidental call panic
// visibly. obs/gate let a caller override the Observer/GateFunc (e.g. to
// wrap PhaseChanged); pass nil for gate when the test never expects a gate.
func noopStepContext(obs Observer, gate GateFunc) StepContext {
	return StepContext{
		Exec:      nil,
		Obs:       obs,
		Artifacts: NoopArtifactSink(),
		Gate:      gate,
		Log:       slog.Default(),
	}
}

// driveGate waits for an EventGateOpened at pos on events and submits dec on
// decisions, correlated by GateID (replaces the pre-WP10 Snapshot/NotifyCh
// polling loop — event delivery is edge-triggered, so unlike the old
// Rev-dedup dance, every EventGateOpened here is a genuinely NEW gate
// opening). Must be launched in a goroutine before RunPipeline runs; cancel
// unblocks the RunPipeline call on timeout.
func driveGate(t *testing.T, events <-chan RunEvent, decisions chan<- GateDecisionIntent, pos HumanGatePosition, dec Decision, timeout time.Duration, cancel context.CancelFunc) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Errorf("driveGate: event bus closed before gate at position %v opened", pos)
				cancel()
				return
			}
			if g, ok := ev.(EventGateOpened); ok && g.Request.Position == pos {
				decisions <- GateDecisionIntent{GateID: g.GateID, Decision: dec}
				return
			}
		case <-timer.C:
			t.Errorf("driveGate timeout waiting for gate at position %v", pos)
			cancel()
			return
		}
	}
}

// runPipelineSync is a convenience wrapper: builds a fresh gate/emitter
// fixture and returns RunPipeline's result+err directly, discarding the
// event bus (only used by tests with no gate in play).
func runPipelineSync(ctx context.Context, setup PipelineSetup, steps PipelineSteps) (Result, error) {
	sc, _, _ := newGateTestContext()
	return RunPipeline(ctx, setup, PipelineRunInput{Prompt: "test prompt", RunID: "test-run"}, sc, steps)
}

// waitRunFinished drains events until EventRunFinished (or channel close),
// returning the terminal Result and error. Fails the test if the channel
// closes without ever delivering EventRunFinished, or the timeout elapses.
func waitRunFinished(t *testing.T, events <-chan RunEvent, timeout time.Duration) (Result, error) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event bus closed before EventRunFinished")
			}
			if fin, ok := ev.(EventRunFinished); ok {
				return fin.Result, fin.Err
			}
		case <-deadline:
			t.Fatal("timed out waiting for EventRunFinished")
		}
	}
}

// collectPhases drains exactly n more RunEvents from events (bounded by
// timeout) and returns the Phase from every EventPhaseStarted seen among
// them, ignoring any other event kind emitted by the same run.
func collectPhases(t *testing.T, events <-chan RunEvent, n int, timeout time.Duration) []Phase {
	t.Helper()
	var phases []Phase
	deadline := time.After(timeout)
	for i := 0; i < n; i++ {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("event bus closed after %d/%d events", i, n)
			}
			if p, ok := ev.(EventPhaseStarted); ok {
				phases = append(phases, p.Phase)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event %d/%d", i+1, n)
		}
	}
	return phases
}
