package qaspec_test

import (
	"testing"

	"github.com/xiii/orqestra/internal/qaspec"
)

// TestSpecIntegrity makes the QA specification falsifiable from `make test`:
// a stale anchor, ledger drift, an untraceable status claim, a line-number
// prose anchor, a test-hygiene violation, or a dead canary all turn this test
// red — the same way a code bug does. The spec defends itself in the standard
// build, not only in the opt-in `make qa-verify` lane.
func TestSpecIntegrity(t *testing.T) {
	root, err := qaspec.RepoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	rep, err := qaspec.Static(root)
	if err != nil {
		t.Fatalf("run static checks: %v", err)
	}
	for _, s := range rep.Soft {
		t.Logf("note: %s", s)
	}
	for _, s := range rep.Hard {
		t.Errorf("spec integrity: %s", s)
	}
}
