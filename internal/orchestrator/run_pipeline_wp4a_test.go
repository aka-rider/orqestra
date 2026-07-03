package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestGateFunc_StaleDecisionNotConsumedAtNextGate is the WP4a/J2 gate proof,
// ported to the WP10 gate mechanism (gate.go's newGateFunc replaces
// Control.Gate): a GateDecisionIntent delivered while NO gate is open (a
// double keypress, or a decision racing a gate's close) must NOT be
// silently handed to the NEXT Gate call — newGateFunc must drain it before
// blocking, so only a FRESH decision, submitted while THIS gate is open,
// satisfies it.
func TestGateFunc_StaleDecisionNotConsumedAtNextGate(t *testing.T) {
	em := newEmitter(64)
	decisions := make(chan GateDecisionIntent, 1)
	gate := newGateFunc(em, decisions)

	// Simulate a decision landing in the buffer with no gate open to receive
	// it. GateID 1 deliberately matches what the FIRST real gate below will
	// be assigned (newGateFunc's sequence counter starts at 1) — this is the
	// case the GateID-mismatch check alone would NOT catch; only draining the
	// stale decision before the gate opens (not just matching IDs) prevents
	// it from satisfying the gate.
	decisions <- GateDecisionIntent{GateID: 1, Decision: Decision{Type: DecisionApprove}}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	type gateResult struct {
		dec Decision
		err error
	}
	resultCh := make(chan gateResult, 1)
	go func() {
		dec, err := gate(ctx, GateRequest{Position: GateAfterDeliberation})
		resultCh <- gateResult{dec, err}
	}()

	select {
	case res := <-resultCh:
		if res.err == nil {
			t.Fatalf("Gate returned decision %+v with no error — a stale pre-gate decision satisfied "+
				"this gate before the user ever saw it (J2)", res.dec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Gate did not return within the grace period")
	}
}

// TestRunPipeline_EditAutoApprove_OneGateCycle is the WP4a/J26 gate proof: a
// confirmed edit (DecisionEdit with AutoApprove and no Comment) is final
// approval. It must NOT route through Revise and re-open the gate for a
// second confirmation — the pipeline's final plan must equal the edited text
// after exactly one gate presentation.
func TestRunPipeline_EditAutoApprove_OneGateCycle(t *testing.T) {
	sc, events, decisions := newGateTestContext()

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
		// EventGateOpened is edge-triggered — every delivery is a genuinely NEW
		// gate opening (unlike the pre-WP10 Snapshot/NotifyCh polling loop,
		// which needed a Rev-based dedup to avoid double-counting the SAME
		// still-open gate observed twice).
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				g, ok := ev.(EventGateOpened)
				if !ok || g.Request.Position != GateAfterDeliberation {
					continue
				}
				gateMu.Lock()
				gateCount++
				first := gateCount == 1
				gateMu.Unlock()
				if first {
					decisions <- GateDecisionIntent{GateID: g.GateID, Decision: Decision{
						Type:          DecisionEdit,
						EditedContent: editedContent,
						AutoApprove:   true,
					}}
				} else {
					// Only reached if the gate incorrectly re-opens (J26) — approve
					// so RunPipeline still completes quickly instead of timing out.
					decisions <- GateDecisionIntent{GateID: g.GateID, Decision: Decision{Type: DecisionApprove}}
				}
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
