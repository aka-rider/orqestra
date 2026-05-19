package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/xiii/orqestra/internal/harness"
)

// ErrBudgetExhausted is returned when a token budget is exceeded.
var ErrBudgetExhausted = errors.New("token budget exhausted")

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
		return fmt.Errorf("%w: used %d of %d", ErrBudgetExhausted, g.usage.TotalUsed(), limit)
	}
	return nil
}

// WrapContinuable returns a ContinuableRunner that enforces the budget
// before and after each call, recording usage under agentID.
func (g *BudgetGuard) WrapContinuable(inner harness.ContinuableRunner, agentID string) harness.ContinuableRunner {
	return &budgetedRunner{inner: inner, guard: g, agentID: agentID}
}

// budgetedRunner is a ContinuableRunner decorator that enforces token budgets.
type budgetedRunner struct {
	inner   harness.ContinuableRunner
	guard   *BudgetGuard
	agentID string
}

func (r *budgetedRunner) RunPrint(ctx context.Context, prompt, systemPrompt string) (harness.RunResult, error) {
	if err := r.guard.Check(); err != nil {
		return harness.RunResult{}, err
	}

	result, innerErr := r.inner.RunPrint(ctx, prompt, systemPrompt)
	r.record(result.Usage)

	if innerErr != nil {
		return result, innerErr
	}
	if err := r.guard.Check(); err != nil {
		return result, err
	}
	return result, nil
}

func (r *budgetedRunner) RunStreaming(ctx context.Context, prompt, systemPrompt string, events chan<- harness.StreamUpdate) (harness.RunResult, error) {
	if err := r.guard.Check(); err != nil {
		return harness.RunResult{}, err
	}

	result, innerErr := r.inner.RunStreaming(ctx, prompt, systemPrompt, events)
	r.record(result.Usage)

	if innerErr != nil {
		return result, innerErr
	}
	if err := r.guard.Check(); err != nil {
		return result, err
	}
	return result, nil
}

func (r *budgetedRunner) RunContinue(ctx context.Context, sessionID, prompt string, events chan<- harness.StreamUpdate) (harness.RunResult, error) {
	if err := r.guard.Check(); err != nil {
		return harness.RunResult{}, err
	}

	result, innerErr := r.inner.RunContinue(ctx, sessionID, prompt, events)
	r.record(result.Usage)

	if innerErr != nil {
		return result, innerErr
	}
	if err := r.guard.Check(); err != nil {
		return result, err
	}
	return result, nil
}

func (r *budgetedRunner) record(usage harness.TokenUsage) {
	if usage.Total() > 0 {
		r.guard.usage.Record(r.agentID, usage.Input, usage.Output)
	}
}
