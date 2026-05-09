package agent

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

type researcherMockRunner struct {
	response string
	err      error
}

func (m *researcherMockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func (m *researcherMockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func TestResearcher_Research_Success(t *testing.T) {
	draft := "## Goal\nBuild something.\n\n## Context\nFound foo.go.\n\n## Draft Steps\n1. Edit foo.go\n\n## Draft Acceptance\n- Tests pass\n\n## Gotchas\nNone.\n\n## Risks\nNone."
	mock := &researcherMockRunner{response: draft}

	cfg := config.ResearcherConfig{
		Model:        "test-model",
		SystemPrompt: "You are a researcher.",
	}

	r := NewResearcher(mock, cfg)
	plan, _, err := r.Research(context.Background(), "build something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != draft {
		t.Errorf("draft mismatch:\ngot:  %s\nwant: %s", plan.Markdown, draft)
	}
}

func TestResearcher_Research_StripsCodeFences(t *testing.T) {
	draft := "## Goal\nBuild.\n\n## Context\nStuff."
	wrapped := "```markdown\n" + draft + "\n```"
	mock := &researcherMockRunner{response: wrapped}

	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)
	plan, _, err := r.Research(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != draft {
		t.Errorf("expected code fences stripped:\ngot:  %q\nwant: %q", plan.Markdown, draft)
	}
}

func TestResearcher_Research_CLIError(t *testing.T) {
	mock := &researcherMockRunner{err: fmt.Errorf("timeout")}

	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)
	_, _, err := r.Research(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error propagation from CLI")
	}
}
