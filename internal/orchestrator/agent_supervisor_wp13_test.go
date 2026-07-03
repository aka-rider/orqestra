package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// modeCapturingExecutor records whether it was invoked with a non-nil input
// channel (interactive/stream-json mode, per hasInputPlane := in != nil in
// exec.go's buildSpecArgs) or nil (one-shot -p mode), then returns
// immediately.
type modeCapturingExecutor struct {
	gotInputPlane bool
}

func (m *modeCapturingExecutor) Run(_ context.Context, _ harness.ProcessSpec, in <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
	m.gotInputPlane = in != nil
	return harness.RunResult{}, nil
}

// TestWP13_SilenceGuardDoesNotFlipInvocationMode is the WP13/J6 args-matrix
// gate: an otherwise-identical spec must produce the IDENTICAL invocation
// mode (interactive input-plane vs one-shot -p) whether or not SilenceGuard
// is configured. Before WP13, needsInputPlane included `len(policies) > 0`,
// so toggling SilenceGuard alone flipped -p <-> --input-format stream-json —
// this test is written to fail on that pre-WP13 formula and pass once
// needsInputPlane depends only on the role-class InputPlane property (plus
// the pre-existing in!=nil / ExpectsReport terms).
func TestWP13_SilenceGuardDoesNotFlipInvocationMode(t *testing.T) {
	guard := NewBudgetGuard(NewRunUsage(0))

	baseSpec := harness.ProcessSpec{
		AgentID: "worker",
		Prompt:  "do work",
		// No InputPlane, no ExpectsReport: a role that should NEVER open an
		// input plane, regardless of policy configuration (WP13/J6).
	}

	withoutGuard := baseSpec
	withGuard := baseSpec
	withGuard.SilenceGuard = harness.SilenceGuardSpec{SilenceSecs: 30, NudgeText: "wake up", MaxNudges: 2}

	execWithout := &modeCapturingExecutor{}
	supWithout := NewAgentSupervisor(execWithout, nil, guard)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := supWithout.Run(ctx, withoutGuard, nil, nil); err != nil {
		t.Fatalf("Run (no SilenceGuard) error: %v", err)
	}

	execWith := &modeCapturingExecutor{}
	supWith := NewAgentSupervisor(execWith, nil, guard)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := supWith.Run(ctx2, withGuard, nil, nil); err != nil {
		t.Fatalf("Run (with SilenceGuard) error: %v", err)
	}

	if execWithout.gotInputPlane != execWith.gotInputPlane {
		t.Errorf("invocation mode differs by SilenceGuard presence alone: without=%v (inputPlane), with=%v (inputPlane) — J6",
			execWithout.gotInputPlane, execWith.gotInputPlane)
	}
	if execWithout.gotInputPlane {
		t.Errorf("expected one-shot -p mode (no InputPlane, no ExpectsReport, no caller-supplied in) in both cases, got interactive input-plane mode")
	}
}

// TestWP13_InputPlaneRoleClassProperty_AlwaysInteractive proves the other
// half of the same invariant: a role WITH InputPlane set opens the input
// plane consistently regardless of SilenceGuard, matching "reporters/executor
// always on" (WP13/J6).
func TestWP13_InputPlaneRoleClassProperty_AlwaysInteractive(t *testing.T) {
	guard := NewBudgetGuard(NewRunUsage(0))

	baseSpec := harness.ProcessSpec{
		AgentID:    "architect",
		Prompt:     "plan it",
		InputPlane: true,
	}

	withoutGuard := baseSpec
	withGuard := baseSpec
	withGuard.SilenceGuard = harness.SilenceGuardSpec{SilenceSecs: 30, NudgeText: "wake up", MaxNudges: 2}

	execWithout := &modeCapturingExecutor{}
	supWithout := NewAgentSupervisor(execWithout, nil, guard)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := supWithout.Run(ctx, withoutGuard, nil, nil); err != nil {
		t.Fatalf("Run (no SilenceGuard) error: %v", err)
	}

	execWith := &modeCapturingExecutor{}
	supWith := NewAgentSupervisor(execWith, nil, guard)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := supWith.Run(ctx2, withGuard, nil, nil); err != nil {
		t.Fatalf("Run (with SilenceGuard) error: %v", err)
	}

	if !execWithout.gotInputPlane || !execWith.gotInputPlane {
		t.Errorf("expected InputPlane:true to open the input plane in both cases, got without=%v with=%v",
			execWithout.gotInputPlane, execWith.gotInputPlane)
	}
}
