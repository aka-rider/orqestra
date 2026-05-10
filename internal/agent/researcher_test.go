package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	draft := "## Goal\nBuild something.\n\n## Context\nFound foo.go.\n\n## Draft Steps\n1. Edit foo.go\n\n## Draft Acceptance\n- Tests pass\n\n## Gotchas\nNone.\n\n## Risks\nNone."
	mock := &researcherMockRunner{response: draft}

	cfg := config.ResearcherConfig{
		Model:        "test-model",
		SystemPrompt: "You are a researcher.",
	}

	r := NewResearcher(mock, cfg)
	plan, _, _, err := r.Research(context.Background(), "build something")
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
	plan, _, _, err := r.Research(context.Background(), "prompt")
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
	_, _, _, err := r.Research(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error propagation from CLI")
	}
}

func TestResearcher_RecoverySkippedWhenOutputHasHeadings(t *testing.T) {
	// Output contains markdown headings — no recovery needed.
	draft := "## Goal\nBuild something."
	mock := &researcherMockRunner{response: draft, sessionID: "some-session"}
	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)

	plan, _, _, err := r.Research(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != draft {
		t.Errorf("got %q, want %q", plan.Markdown, draft)
	}
}

func TestResearcher_RecoverySkippedWithoutSessionID(t *testing.T) {
	// No markdown headings, but no session ID → return raw output.
	mock := &researcherMockRunner{response: "I saved the plan to a file."}
	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)

	plan, _, _, err := r.Research(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != "I saved the plan to a file." {
		t.Errorf("got %q", plan.Markdown)
	}
}

func TestResearcher_RecoverySuccess(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	sessionID := "researcher-recovery-session"
	planMD := "## Goal\nRecovered draft.\n\n## Context\nFound stuff."

	// Create plan file under ~/.claude/plans/
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(plansDir, "recovered-draft.md")
	if err := os.WriteFile(planFile, []byte(planMD), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create session JSONL referencing the plan file.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(tmp, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	jsonlContent := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, planFile)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &researcherMockRunner{
		response:  "I have saved the plan to the plan file.",
		sessionID: sessionID,
	}
	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)

	plan, _, sid, err := r.Research(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("expected recovery to succeed, got: %v", err)
	}
	if plan.Markdown != planMD {
		t.Errorf("recovered plan mismatch:\ngot:  %s\nwant: %s", plan.Markdown, planMD)
	}
	if sid != sessionID {
		t.Errorf("session ID = %q, want %q", sid, sessionID)
	}
}

func TestResearcher_RecoveryFailsGracefully(t *testing.T) {
	// No headings, session ID present but no matching session log → returns raw output.
	mock := &researcherMockRunner{
		response:  "Saved the plan.",
		sessionID: "nonexistent-session-12345",
	}
	cfg := config.ResearcherConfig{Model: "test"}
	r := NewResearcher(mock, cfg)

	plan, _, _, err := r.Research(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return the original output when recovery fails.
	if plan.Markdown != "Saved the plan." {
		t.Errorf("got %q", plan.Markdown)
	}
}
