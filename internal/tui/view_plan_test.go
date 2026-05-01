package tui

import (
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/types"
)

func TestPlanView_RendersGoal(t *testing.T) {
	spec := types.Specification{
		Goal:       "Deploy the application",
		Steps:      []string{"Build", "Test", "Deploy"},
		Acceptance: []string{"All tests pass", "App responds to healthcheck"},
	}

	pv := newPlanView(spec)
	rendered := pv.renderPlan()

	if !strings.Contains(rendered, "Deploy the application") {
		t.Error("plan view should contain the goal")
	}
	if !strings.Contains(rendered, "Build") {
		t.Error("plan view should contain steps")
	}
	if !strings.Contains(rendered, "All tests pass") {
		t.Error("plan view should contain acceptance criteria")
	}
}

func TestPlanView_RendersStepNumbers(t *testing.T) {
	spec := types.Specification{
		Goal:  "Test",
		Steps: []string{"First", "Second", "Third"},
	}

	pv := newPlanView(spec)
	rendered := pv.renderPlan()

	if !strings.Contains(rendered, "1. First") {
		t.Error("expected numbered step 1")
	}
	if !strings.Contains(rendered, "3. Third") {
		t.Error("expected numbered step 3")
	}
}
