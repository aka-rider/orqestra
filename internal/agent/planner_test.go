package agent

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// plannerMockCLIRunner is a test double for the CLIRunner interface.
type plannerMockCLIRunner struct {
	response string
	err      error
}

func (m *plannerMockCLIRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func (m *plannerMockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func TestPlanner_Refine_Success(t *testing.T) {
	planMD := "# Plan\n\n## Goal\nBuild a thing.\n\n## Work Packages\n\n### 1. Do stuff\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"
	mock := &plannerMockCLIRunner{response: planMD}

	cfg := config.PlannerConfig{
		Model:        "test-model",
		SystemPrompt: "You are the architect.",
	}

	p := NewPlanner(mock, cfg)
	plan, _, _, err := p.Refine(context.Background(), "some researcher draft")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != planMD {
		t.Errorf("plan markdown mismatch:\ngot:  %s\nwant: %s", plan.Markdown, planMD)
	}
}

func TestPlanner_Refine_MissingPlanHeader(t *testing.T) {
	mock := &plannerMockCLIRunner{response: "## Goal\nDo something\n\n## Work Packages\n..."}

	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)
	_, _, _, err := p.Refine(context.Background(), "draft")
	if err == nil {
		t.Fatal("expected error for missing '# Plan' header")
	}
}

func TestPlanner_Refine_MissingWorkPackages(t *testing.T) {
	mock := &plannerMockCLIRunner{response: "# Plan\n\n## Goal\nDo something"}

	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)
	_, _, _, err := p.Refine(context.Background(), "draft")
	if err == nil {
		t.Fatal("expected error for missing '## Work Packages' section")
	}
}

func TestPlanner_Refine_CodeFenceStripping(t *testing.T) {
	planMD := "# Plan\n\n## Goal\nBuild.\n\n## Work Packages\n\n### 1. Do"
	wrapped := "```markdown\n" + planMD + "\n```"
	mock := &plannerMockCLIRunner{response: wrapped}

	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)
	plan, _, _, err := p.Refine(context.Background(), "draft")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != planMD {
		t.Errorf("expected code fences stripped:\ngot:  %q\nwant: %q", plan.Markdown, planMD)
	}
}

func TestPlanner_Refine_CLIError(t *testing.T) {
	mock := &plannerMockCLIRunner{err: fmt.Errorf("connection refused")}

	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)
	_, _, _, err := p.Refine(context.Background(), "draft")
	if err == nil {
		t.Fatal("expected error propagation from CLI")
	}
}

func TestPlanner_RefineWithComments(t *testing.T) {
	planMD := "# Plan\n\n## Goal\nRevised.\n\n## Work Packages\n\n### 1. Fixed"
	mock := &plannerMockCLIRunner{response: planMD}

	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)
	plan, _, _, err := p.RefineWithComments(context.Background(), "old plan", "please fix step 2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != planMD {
		t.Errorf("plan markdown mismatch")
	}
}
