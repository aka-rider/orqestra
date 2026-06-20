package orchestrator

import (
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
