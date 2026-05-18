package orchestrator

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

type stubRunner struct {
	result harness.RunResult
	err    error
}

func (s *stubRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	return s.result, s.err
}

func (s *stubRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return s.result, s.err
}

func (s *stubRunner) RunContinue(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return s.result, s.err
}

func TestBudgetGuard_Unlimited(t *testing.T) {
	u := NewRunUsage(0)
	g := NewBudgetGuard(u)

	if err := g.Check(); err != nil {
		t.Errorf("Check() with unlimited budget: %v", err)
	}
}

func TestBudgetGuard_UnderBudget(t *testing.T) {
	u := NewRunUsage(1000)
	u.StartAgent("test", AgentMeta{})
	u.Record("test", 200, 100)

	g := NewBudgetGuard(u)
	if err := g.Check(); err != nil {
		t.Errorf("Check() under budget: %v", err)
	}
}

func TestBudgetGuard_OverBudget(t *testing.T) {
	u := NewRunUsage(500)
	u.StartAgent("test", AgentMeta{})
	u.Record("test", 300, 300)

	g := NewBudgetGuard(u)
	err := g.Check()
	if err == nil {
		t.Fatal("Check() should return error when over budget")
	}
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("error should be ErrBudgetExhausted, got: %v", err)
	}
}

func TestBudgetedRunner_PreCheck(t *testing.T) {
	u := NewRunUsage(100)
	u.StartAgent("worker", AgentMeta{})
	u.Record("worker", 50, 60) // over budget

	g := NewBudgetGuard(u)
	inner := &stubRunner{result: harness.RunResult{Usage: harness.TokenUsage{Input: 10, Output: 5}}}
	wrapped := g.WrapContinuable(inner, "worker")

	_, err := wrapped.RunPrint(context.Background(), "test", "")
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("expected pre-check to block, got: %v", err)
	}
}

func TestBudgetedRunner_PostCheck(t *testing.T) {
	u := NewRunUsage(100)
	u.StartAgent("worker", AgentMeta{})
	// 80 used, budget 100 — pre-check passes
	u.Record("worker", 40, 40)

	g := NewBudgetGuard(u)
	// Inner call returns 30 tokens — post-call total = 80 + 30 = 110 > 100
	inner := &stubRunner{result: harness.RunResult{Usage: harness.TokenUsage{Input: 20, Output: 10}}}
	wrapped := g.WrapContinuable(inner, "worker")

	_, err := wrapped.RunPrint(context.Background(), "test", "")
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("expected post-check to fail, got: %v", err)
	}
}

func TestBudgetedRunner_InnerErrorTakesPrecedence(t *testing.T) {
	u := NewRunUsage(1000)
	u.StartAgent("worker", AgentMeta{})

	g := NewBudgetGuard(u)
	innerErr := errors.New("connection refused")
	inner := &stubRunner{
		result: harness.RunResult{Usage: harness.TokenUsage{Input: 50, Output: 50}},
		err:    innerErr,
	}
	wrapped := g.WrapContinuable(inner, "worker")

	_, err := wrapped.RunPrint(context.Background(), "test", "")
	if !errors.Is(err, innerErr) {
		t.Errorf("expected inner error, got: %v", err)
	}

	// Usage should still be recorded
	snap := u.Snapshot()
	if snap.Input != 50 {
		t.Errorf("input = %d, want 50 (usage recorded despite inner error)", snap.Input)
	}
}

func TestBudgetedRunner_RecordsUsage(t *testing.T) {
	u := NewRunUsage(0)
	u.StartAgent("worker", AgentMeta{})

	g := NewBudgetGuard(u)
	inner := &stubRunner{result: harness.RunResult{Usage: harness.TokenUsage{Input: 100, Output: 50}}}
	wrapped := g.WrapContinuable(inner, "worker")

	_, err := wrapped.RunStreaming(context.Background(), "test", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := u.Snapshot()
	if snap.Input != 100 || snap.Output != 50 {
		t.Errorf("snap = %d/%d, want 100/50", snap.Input, snap.Output)
	}
}
