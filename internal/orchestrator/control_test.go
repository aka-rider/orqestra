package orchestrator

import (
	"context"
	"testing"
	"time"
)

// TestControl_StaleDecisionNotConsumedAtNextGate is the WP4a/J2 gate proof:
// a Decision submitted while no gate is open (double keypress, or Submit
// racing a gate close) must NOT be silently handed to the NEXT Gate call.
// Gate must drain any stale buffered decision before it starts waiting, so a
// fresh Submit — not a leftover one — is what satisfies it.
func TestControl_StaleDecisionNotConsumedAtNextGate(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)

	// Simulate a decision landing in the buffer with no gate open to receive it.
	ctrl.Submit(Decision{Type: DecisionApprove})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	type gateResult struct {
		dec Decision
		err error
	}
	resultCh := make(chan gateResult, 1)
	go func() {
		dec, err := ctrl.Gate(ctx, GateRequest{Position: GateAfterDeliberation})
		resultCh <- gateResult{dec, err}
	}()

	select {
	case res := <-resultCh:
		if res.err == nil {
			t.Fatalf("Gate returned decision %+v with no error — a stale pre-gate Submit satisfied "+
				"this gate before the user ever saw it (J2)", res.dec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Gate did not return within the grace period")
	}
}
