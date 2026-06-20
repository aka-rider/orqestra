package orchestrator

import (
	"context"

	"github.com/xiii/orqestra/internal/harness"
)

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

// Wrap returns a Runner that enforces the budget
// before and after each call, recording usage under agentID.
func (g *BudgetGuard) Wrap(inner harness.Runner, agentID string) harness.Runner {
	return &budgetedRunner{inner: inner, guard: g, agentID: agentID, usage: g.usage}
}

// budgetedRunner is a Runner decorator that enforces token budgets
// and records per-agent usage under the orchestrator's RunUsage.
type budgetedRunner struct {
	inner   harness.Runner
	guard   *BudgetGuard
	agentID string
	usage   *RunUsage
}

func (r *budgetedRunner) Post(msg string) {
	r.inner.Post(msg)
}

func (r *budgetedRunner) Receive() <-chan harness.Event {
	ch := make(chan harness.Event, 256)
	inner := r.inner.Receive()

	go func() {
		defer close(ch)
		for ev := range inner {
			// Record per-agent usage for usage events.
			if ev.Kind == harness.EventUsage {
				r.usage.Record(r.agentID, ev.Input, ev.Output)
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}()

	return ch
}

func (r *budgetedRunner) ExtractPlan(ctx context.Context) (string, error) {
	return r.inner.ExtractPlan(ctx)
}

func (r *budgetedRunner) SessionID() string {
	return r.inner.SessionID()
}

func (r *budgetedRunner) Cancel() error {
	return r.inner.Cancel()
}
