package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"
)

// completer is satisfied by any Observer that still exposes the pre-WP2
// Complete(RunStatus) method. Once WP2 deletes Complete from Observer and
// ObsStore, no concrete Observer implementation satisfies this interface —
// the type assertion in gateObs.Complete below is simply never true, and
// gateObs.Complete becomes unreachable dead code (nothing calls it, because
// RunPipeline no longer calls sc.Obs.Complete at all). This lets the same
// test file compile and run meaningfully both before and after the fix.
type completer interface {
	Complete(RunStatus)
}

// gateObs wraps an Observer and, if the wrapped Observer still implements the
// pre-WP2 Complete(RunStatus) method, performs the real write and then blocks
// the caller until the test signals proceed. This gives a concurrently
// polling reader a deterministic window in which to observe the intermediate,
// status-only terminal write BEFORE the real Finished(Result, error) write
// lands — reproducing J3/J23 without relying on incidental goroutine timing.
type gateObs struct {
	Observer
	proceed chan struct{}
}

func (g *gateObs) Complete(status RunStatus) {
	if c, ok := g.Observer.(completer); ok {
		c.Complete(status)
	}
	<-g.proceed
}

// TestSingleTerminalWrite_NoExecute drives a plan-only pipeline
// (Execution: false) through the same sequence startNew uses in production —
// RunPipeline followed by obs.Finished(result, err) (engine_pipeline.go:126-
// 135) — and asserts that the FIRST snapshot observing Terminal.Done carries
// the real Result (FinalPlan non-empty, RunDir set), never a partial
// status-only write (J3/J23).
func TestSingleTerminalWrite_NoExecute(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)

	proceed := make(chan struct{})
	var proceedOnce sync.Once
	release := func() { proceedOnce.Do(func() { close(proceed) }) }

	gated := &gateObs{Observer: obs, proceed: proceed}
	sc := noopStepContext(gated, ctrl)

	setup := PipelineSetup{Execution: false, Validation: false, HumanGates: nil}
	steps := defaultTestSteps()

	const wantRunDir = "/fake/run/dir"

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		result, err := RunPipeline(context.Background(), setup,
			PipelineRunInput{Prompt: "test", RunID: "run-1"}, sc, steps)
		// Mirror startNew (engine_pipeline.go:134-135): RunDir is stamped
		// after RunPipeline returns, then Finished is the terminal write.
		result.RunDir = wantRunDir
		obs.Finished(result, err)
	}()

	// Poll for the FIRST Terminal.Done observation. While RunPipeline is
	// blocked inside gateObs.Complete (pre-WP2 only), this tight loop has a
	// deterministic window in which to observe the intermediate write.
	var first ObsSnapshot
	found := false
	deadline := time.Now().Add(5 * time.Second)
	for !found {
		snap := obs.Snapshot()
		if snap.Terminal.Done {
			first = snap
			found = true
			break
		}
		if time.Now().After(deadline) {
			release()
			t.Fatal("timeout waiting for Terminal.Done")
		}
	}
	release() // unblock gateObs.Complete if it's waiting (pre-WP2); no-op post-WP2
	<-runDone

	if first.Terminal.Result.FinalPlan == "" {
		t.Errorf("first observed Terminal.Done has empty FinalPlan (partial terminal write): %+v", first.Terminal)
	}
	if first.Terminal.Result.RunDir == "" {
		t.Errorf("first observed Terminal.Done has empty RunDir (partial terminal write): %+v", first.Terminal)
	}
}
