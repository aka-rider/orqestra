//go:build qacanary && darwin && integration

package harness_test

import (
	"testing"
	"time"
)

// TestCanary_DEFECT01_ReceiveNeverClosesAfterExit asserts DEFECT-01 / INV-H1-CLOSE
// is still live: the sandboxed runner forwards events but never closes Receive()'s
// channel, so a `for range` consumer hangs after the process exits.
//
// Run by `make qa-verify` (canary lane). It must PASS while the defect is live.
// When it starts to FAIL, the defect was fixed: retire this canary and unskip the
// gate TestHarnessRunner_ReceiveClosesOnExit (flip INV-H1-CLOSE to covered).
//
// Tagged `qacanary` so it never runs in the normal sandbox suite — a passing test
// that asserts a bug exists belongs only in the canary lane.
func TestCanary_DEFECT01_ReceiveNeverClosesAfterExit(t *testing.T) {
	events, closed := driveRunnerUntilClose(t, 3*time.Second)
	if events < 1 {
		t.Fatalf("canary inconclusive: replay stub produced no events (not the hang under test)")
	}
	if closed {
		t.Fatalf("DEFECT-01 appears FIXED: Receive() channel closed. Retire this canary and " +
			"unskip gate TestHarnessRunner_ReceiveClosesOnExit (INV-H1-CLOSE).")
	}
}
