package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestRunPipeline_EditAutoApprove_OneGateCycle is the WP4a/J26 gate proof: a
// confirmed edit (DecisionEdit with AutoApprove and no Comment) is final
// approval. It must NOT route through Revise and re-open the gate for a
// second confirmation — the pipeline's final plan must equal the edited text
// after exactly one gate presentation.
func TestRunPipeline_EditAutoApprove_OneGateCycle(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := noopStepContext(obs, ctrl)

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	steps := defaultTestSteps()

	editedContent := "# Plan\n\n## Goal\nEdited via AutoApprove.\n\n## Work Packages\n\n" +
		"### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	gateCount := 0
	var gateMu sync.Mutex
	go func() {
		// Edge-triggered via Rev, not via observing a HasGate true→false→true
		// transition: NotifyCh is a coalescing wake signal and Snapshot is a
		// point-in-time poll, so a fast open→close→reopen can skip the closed
		// window entirely between two polls. GateOpened always bumps Rev, so a
		// distinct gate opening always carries a Rev this driver hasn't handled
		// yet — even if the closed state between two openings was never observed
		// — while redundant wakeups on the SAME still-open gate keep the same
		// Rev and are correctly ignored.
		var lastHandledRev uint64
		handled := false
		for {
			snap := obs.Snapshot()
			if snap.HasGate && snap.Gate.Position == GateAfterDeliberation && (!handled || snap.Rev != lastHandledRev) {
				handled = true
				lastHandledRev = snap.Rev
				gateMu.Lock()
				gateCount++
				first := gateCount == 1
				gateMu.Unlock()
				if first {
					ctrl.Submit(Decision{
						Type:          DecisionEdit,
						EditedContent: editedContent,
						AutoApprove:   true,
					})
				} else {
					// Only reached if the gate incorrectly re-opens (J26) — approve
					// so RunPipeline still completes quickly instead of timing out.
					ctrl.Submit(Decision{Type: DecisionApprove})
				}
			}
			select {
			case <-obs.NotifyCh():
			case <-ctx.Done():
				return
			}
		}
	}()

	result, err := RunPipeline(ctx, setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, StatusSuccess)
	}
	if result.FinalPlan != editedContent {
		t.Errorf("FinalPlan = %q, want the edited content %q", result.FinalPlan, editedContent)
	}

	gateMu.Lock()
	defer gateMu.Unlock()
	if gateCount != 1 {
		t.Errorf("gateCount = %d, want exactly 1 (AutoApprove edit must not re-open the gate, J26)", gateCount)
	}
}
