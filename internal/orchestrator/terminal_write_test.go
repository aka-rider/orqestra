package orchestrator

import (
	"context"
	"testing"
	"time"
)

// TestSingleTerminalWrite_NoExecute drives a plan-only pipeline
// (Execution: false) through the same sequence startNew uses in production —
// RunPipeline followed by obs.Finished(result, err) (engine_pipeline.go's
// finish closure) — and asserts that EventRunFinished carries the real
// Result (FinalPlan non-empty, RunDir set), never a partial status-only
// write (J3/J23).
//
// WP10 note: this invariant is now structurally guaranteed by the emitter's
// contract (EventRunFinished is emitted exactly once, atomically, from a
// single Finished(res, err) call — there is no longer a separate
// status-only write to race against, since ObsStore/Control's terminal
// snapshot mutation no longer exists). This test still exercises the real
// end-to-end sequence rather than asserting the invariant only by reading
// the emitter's doc comment.
func TestSingleTerminalWrite_NoExecute(t *testing.T) {
	em := newEmitter(context.Background(), 64)
	obs := newEventObserver(em)
	sc := noopStepContext(obs, nil)

	setup := PipelineSetup{Execution: false, Validation: false, HumanGates: nil}
	steps := defaultTestSteps()

	const wantRunDir = "/fake/run/dir"

	go func() {
		result, err := RunPipeline(context.Background(), setup,
			PipelineRunInput{Prompt: "test", RunID: "run-1"}, sc, steps)
		// Mirror startNew: RunDir is stamped after RunPipeline returns, then
		// Finished is the terminal write.
		result.RunDir = wantRunDir
		obs.Finished(result, err)
	}()

	result, _ := waitRunFinished(t, em.Events(), 5*time.Second)

	if result.FinalPlan == "" {
		t.Errorf("EventRunFinished has empty FinalPlan (partial terminal write): %+v", result)
	}
	if result.RunDir == "" {
		t.Errorf("EventRunFinished has empty RunDir (partial terminal write): %+v", result)
	}
}
