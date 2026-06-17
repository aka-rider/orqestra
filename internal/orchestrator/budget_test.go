package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

type stubRunner struct {
	events chan harness.Event
	done   chan struct{}
}

func (s *stubRunner) Post(msg string) {
	// Simulate a response
	if s.events == nil {
		return
	}
	select {
	case s.events <- harness.Event{Kind: harness.EventChunk, Text: "response"}:
	case <-s.done:
	}
	select {
	case s.events <- harness.Event{Kind: harness.EventUsage, Input: 10, Output: 5}:
	case <-s.done:
	}
	select {
	case s.events <- harness.Event{Kind: harness.EventSessionDone}:
	case <-s.done:
	}
	close(s.events)
}

func (s *stubRunner) Receive() <-chan harness.Event {
	return s.events
}

func (s *stubRunner) ExtractPlan(ctx context.Context) (string, error) {
	return "plan content", nil
}

func (s *stubRunner) SetEvents(ch chan<- harness.Event) {
	// Create the events channel that Post() writes to.
	// The injected ch is send-only (runner writes to it); we don't range over it.
	if s.events == nil {
		s.events = make(chan harness.Event, 256)
	}
}

func (s *stubRunner) SessionID() string {
	return "test-session"
}

func (s *stubRunner) Cancel() error {
	close(s.done)
	return nil
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
	if !errors.Is(err, harness.ErrBudgetExhausted) {
		t.Errorf("error should be ErrBudgetExhausted, got: %v", err)
	}
}

func TestBudgetedRunner_RecordsUsage(t *testing.T) {
	t.Skip("budgetedRunner removed — budget tracking moved into budgetExecutor")
}
