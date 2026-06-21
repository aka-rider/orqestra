//go:build unix

package qarun

import (
	"context"
	"testing"
	"time"
)

// TestRun_GreenOnSuccess: a command that exits 0 → GREEN.
func TestRun_GreenOnSuccess(t *testing.T) {
	r, err := Run(context.Background(), RunSpec{Name: "true", Deadline: 5 * time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Outcome != Green || r.ExitCode != 0 {
		t.Fatalf("got %v exit=%d, want GREEN exit=0", r.Outcome, r.ExitCode)
	}
}

// TestRun_RedOnFailure: a command that exits non-zero → RED.
func TestRun_RedOnFailure(t *testing.T) {
	r, err := Run(context.Background(), RunSpec{Name: "false", Deadline: 5 * time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Outcome != Red || r.ExitCode == 0 {
		t.Fatalf("got %v exit=%d, want RED exit!=0", r.Outcome, r.ExitCode)
	}
}

// TestRun_NoVerdictOnHang is the keystone for INV-HARNESS-VERDICT: a hanging
// command must be killed at the deadline and reported as NO-VERDICT, promptly —
// never an indefinite hang. This makes "hang → bounded red" itself falsifiable.
func TestRun_NoVerdictOnHang(t *testing.T) {
	start := time.Now()
	r, err := Run(context.Background(), RunSpec{
		Name:     "sleep",
		Args:     []string{"30"},
		Deadline: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Outcome != NoVerdict {
		t.Fatalf("got %v, want NO-VERDICT for a hanging command", r.Outcome)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v — it waited for the hang instead of killing at the deadline", elapsed)
	}
}
