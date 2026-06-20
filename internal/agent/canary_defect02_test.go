//go:build qacanary

package agent

import "testing"

// Canary for DEFECT-02 / INV-P3-VALID (see docs/qa/qa-spec.md §4).
//
// It asserts the bug is STILL LIVE: validation output that errored, was skipped,
// or simply ignored the marker protocol parses to VerdictPass — which the engine
// maps to StatusSuccess (engine.go: status starts StatusSuccess, only VerdictFail
// flips it). So a run that proved nothing is reported as success.
//
// This canary is run by `make qa-verify` and must PASS while the defect is live.
// When it starts to FAIL, the defect was fixed: retire this canary, add the real
// gate (a test that the orchestrator refuses to map no-evidence to success), and
// flip INV-P3-VALID to `covered` in docs/qa/invariants.yaml.
//
// Tagged `qacanary` so it never runs in the normal suite — known bugs must not
// turn `make test` red; they are tracked by the canary lane instead.
func TestCanary_DEFECT02_EmptyValidationParsesPass(t *testing.T) {
	if got := ParseValidationOutput("").Verdict; got != VerdictPass {
		t.Fatalf("DEFECT-02 appears fixed: empty validation now parses to %q (was VerdictPass). "+
			"Retire this canary and add gate INV-P3-VALID.", got)
	}
	marklessProse := "I ran the build and everything looks good to me."
	if got := ParseValidationOutput(marklessProse).Verdict; got != VerdictPass {
		t.Fatalf("DEFECT-02 appears fixed: marker-less output now parses to %q (was VerdictPass).", got)
	}
}
