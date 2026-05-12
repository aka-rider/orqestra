package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

type researcherMockRunner struct {
	response  string
	sessionID string
	err       error
}

func (m *researcherMockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response, SessionID: m.sessionID}, nil
}

func (m *researcherMockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response, SessionID: m.sessionID}, nil
}

func TestResearcher_Research_Success(t *testing.T) {
	sessionID := "test-researcher-success"
	draft := "## Goal\nBuild something.\n\n## Codebase Facts\n- foo.go exists, contains X\n\n## Constraints Discovered\n- None\n\n## Open Questions\n- None"
	setupPlanFile(t, sessionID, draft)

	mock := &researcherMockRunner{response: "saved", sessionID: sessionID}
	cfg := config.ResearcherConfig{
		Model:        "test-model",
		SystemPrompt: "You are a researcher.",
	}

	r := NewResearcher(mock, cfg)
	plan, _, sid, err := r.Research(context.Background(), "build something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := draft
	if plan.Markdown != expected {
		t.Errorf("draft mismatch:\ngot:  %s\nwant: %s", plan.Markdown, expected)
	}
	if sid != sessionID {
		t.Errorf("session ID = %q, want %q", sid, sessionID)
	}
}

func TestResearcher_Research_CLIError(t *testing.T) {
	mock := &researcherMockRunner{err: fmt.Errorf("timeout")}

	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)
	_, _, _, err := r.Research(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error propagation from CLI")
	}
}

func TestResearcher_ErrorWithoutSessionID(t *testing.T) {
	mock := &researcherMockRunner{response: "I saved the plan to a file."}
	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)

	_, _, _, err := r.Research(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error when no session ID is present")
	}
	if !strings.Contains(err.Error(), "no session ID") {
		t.Errorf("expected 'no session ID' error, got: %v", err)
	}
}

func TestResearcher_PlanFileExtraction(t *testing.T) {
	sessionID := "researcher-extraction-session"
	planMD := "## Goal\nRecovered draft.\n\n## Codebase Facts\nFound stuff."
	setupPlanFile(t, sessionID, planMD)

	mock := &researcherMockRunner{
		response:  "I have saved the plan to the plan file.",
		sessionID: sessionID,
	}
	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)

	plan, _, sid, err := r.Research(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("expected plan file extraction to succeed, got: %v", err)
	}
	// Content starts with ## Goal, normalization removed, so check directly.
	if !strings.HasPrefix(plan.Markdown, "## Goal") {
		t.Errorf("expected header, got: %s", truncateRaw(plan.Markdown, 50))
	}
	if sid != sessionID {
		t.Errorf("session ID = %q, want %q", sid, sessionID)
	}
}

func TestResearcher_ErrorWithMissingJSONL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	mock := &researcherMockRunner{
		response:  "Saved the plan.",
		sessionID: "nonexistent-session-12345",
	}
	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)

	_, _, _, err := r.Research(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error when JSONL is missing")
	}
}
