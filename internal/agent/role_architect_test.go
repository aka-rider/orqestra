package agent_test

// INV-ROLE-ARCH: the architect agent's real capability gate.
//
// The architect's load-bearing job is to deliver a plan the worker executes. Its
// real, security-critical capability is secure plan sourcing: a plan written
// under ~/.claude/plans is delivered verbatim, and any path OUTSIDE that root is
// rejected — a stale or injected plan from elsewhere must never reach the worker.
// This drives the real agent.ReadPlanFile against real files through the real
// security gate (no fakes). A regression in the prefix gate or the read path
// turns this RED in seconds.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/agent"
)

func TestRole_Architect_PlanFileSecurity(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	plansDir := filepath.Join(tmpHome, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(plansDir, "sess-plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\nDo the work verbatim."), 0o644); err != nil {
		t.Fatal(err)
	}

	// In-root plan is delivered verbatim to the worker boundary.
	got, err := agent.ReadPlanFile("sess", planPath, tmpHome)
	if err != nil {
		t.Fatalf("INV-ROLE-ARCH: secure plan read failed for an in-root plan: %v", err)
	}
	if !strings.Contains(got, "Do the work verbatim.") {
		t.Fatalf("INV-ROLE-ARCH: plan content not delivered verbatim, got: %q", got)
	}

	// Out-of-root plan path is rejected — never leaks a plan from outside ~/.claude/plans.
	evil := filepath.Join(t.TempDir(), "evil-plan.md")
	if err := os.WriteFile(evil, []byte("# Evil\nInjected plan."), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ReadPlanFile("sess", evil, tmpHome); err == nil {
		t.Fatal("INV-ROLE-ARCH: SECURITY FAILURE — out-of-root plan path was accepted")
	}
}
