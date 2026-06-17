package orchestrator

import (
	"context"

	"github.com/xiii/orqestra/internal/harness"
)

// budgetExecutor wraps a harness.Executor and enforces a token budget.
// Unlike budgetedRunner, it records usage from RunResult.Usage post-hoc —
// no forwarding goroutine, no lossy event drop.
type budgetExecutor struct {
	inner   harness.Executor
	guard   *BudgetGuard
	agentID string
	usage   *RunUsage
}

// NewBudgetExecutor returns an Executor that enforces the budget before and
// after each Run call, recording usage from res.Usage (no goroutine needed).
func NewBudgetExecutor(inner harness.Executor, guard *BudgetGuard, agentID string) harness.Executor {
	return &budgetExecutor{inner: inner, guard: guard, agentID: agentID, usage: guard.usage}
}

func (b *budgetExecutor) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if err := b.guard.Check(); err != nil {
		return harness.RunResult{}, err
	}

	res, err := b.inner.Run(ctx, spec, in, sink)

	// Record usage post-hoc from the returned value — no forwarding goroutine.
	if res.Usage.Input > 0 || res.Usage.Output > 0 {
		b.usage.Record(b.agentID, res.Usage.Input, res.Usage.Output)
	}

	// Check budget after — if now exhausted, preserve the result and wrap error.
	if checkErr := b.guard.Check(); checkErr != nil && err == nil {
		return res, checkErr
	}

	return res, err
}

// BudgetGuard enforces token budgets by reading from RunUsage.
type BudgetGuard struct {
	usage *RunUsage
}

// NewBudgetGuard creates a BudgetGuard backed by the given RunUsage.
func NewBudgetGuard(usage *RunUsage) *BudgetGuard {
	return &BudgetGuard{usage: usage}
}

// Check returns ErrBudgetExhausted if the budget is exceeded.
// Returns nil if the limit is 0 (unlimited) or usage is under budget.
func (g *BudgetGuard) Check() error {
	limit := g.usage.Limit()
	if limit == 0 {
		return nil
	}
	if g.usage.TotalUsed() >= limit {
		return harness.ErrBudgetExhausted
	}
	return nil
}

