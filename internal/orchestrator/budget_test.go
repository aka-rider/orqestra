package orchestrator

import (
	"errors"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

func TestBudgetGuard_Unlimited(t *testing.T) {
	u := NewRunUsage(0)
	g := NewBudgetGuard(u)

	if err := g.Check(); err != nil {
		t.Errorf("Check() with unlimited budget: %v", err)
	}
}

func TestBudgetGuard_UnderBudget(t *testing.T) {
	u := NewRunUsage(1000)
	u.Record("test", 200, 100)

	g := NewBudgetGuard(u)
	if err := g.Check(); err != nil {
		t.Errorf("Check() under budget: %v", err)
	}
}

func TestBudgetGuard_OverBudget(t *testing.T) {
	u := NewRunUsage(500)
	u.Record("test", 300, 300)

	g := NewBudgetGuard(u)
	err := g.Check()
	if err == nil {
		t.Fatal("Check() should return error when over budget")
	}
	if !errors.Is(err, harness.ErrBudgetExhausted) {
		t.Errorf("error should be ErrBudgetExhausted, got: %v", err)
	}
}

func TestBudgetedRunner_RecordsUsage(t *testing.T) {
	t.Skip("budgetedRunner removed — budget tracking moved into budgetExecutor")
}
