package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// architectMockCLIRunner is a test double for the CLIRunner interface.
type architectMockCLIRunner struct {
	response  string
	sessionID string
	err       error
}

func (m *architectMockCLIRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response, SessionID: m.sessionID}, nil
}

func (m *architectMockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response, SessionID: m.sessionID}, nil
}

func TestArchitect_Refine_Success(t *testing.T) {
	planMD := "# Plan\n\n## Goal\nBuild a thing.\n\n## Work Packages\n\n### 1. Do stuff\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"
	sessionID := "test-refine-success"
	setupPlanFile(t, sessionID, planMD)

	mock := &architectMockCLIRunner{response: "plan saved", sessionID: sessionID}
	cfg := config.ArchitectConfig{
		Model:        "test-model",
		SystemPrompt: "You are the architect.",
	}

	p := NewArchitect(mock, cfg)
	plan, _, sid, err := p.Refine(context.Background(), "user prompt", PromptBrief{Task: "test"}, "some researcher draft")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != planMD {
		t.Errorf("plan markdown mismatch:\ngot:  %s\nwant: %s", plan.Markdown, planMD)
	}
	if sid != sessionID {
		t.Errorf("session ID = %q, want %q", sid, sessionID)
	}
}

func TestArchitect_Refine_EmptyPlan(t *testing.T) {
	sessionID := "test-empty-plan"
	setupPlanFile(t, sessionID, "   \n  \t  ")

	mock := &architectMockCLIRunner{response: "done", sessionID: sessionID}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)
	_, _, _, err := p.Refine(context.Background(), "prompt", PromptBrief{}, "draft")
	if err == nil {
		t.Fatal("expected error for empty plan file")
	}
}

func TestArchitect_Refine_CLIError(t *testing.T) {
	mock := &architectMockCLIRunner{err: fmt.Errorf("connection refused")}

	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)
	_, _, _, err := p.Refine(context.Background(), "prompt", PromptBrief{}, "draft")
	if err == nil {
		t.Fatal("expected error propagation from CLI")
	}
}

func TestArchitect_RefineWithComments(t *testing.T) {
	planMD := "# Plan\n\n## Goal\nRevised.\n\n## Work Packages\n\n### 1. Fixed"
	sessionID := "test-refine-comments"
	setupPlanFile(t, sessionID, planMD)

	mock := &architectMockCLIRunner{response: "revised", sessionID: sessionID}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)
	plan, _, _, err := p.RefineWithComments(context.Background(), "old plan", "please fix step 2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Markdown != planMD {
		t.Errorf("plan markdown mismatch")
	}
}

func TestArchitect_ErrorWithoutSessionID(t *testing.T) {
	mock := &architectMockCLIRunner{response: "no plan header here"}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)

	_, _, _, err := p.Refine(context.Background(), "prompt", PromptBrief{}, "draft")
	if err == nil {
		t.Fatal("expected error when no session ID is present")
	}
	if !strings.Contains(err.Error(), "no session ID") {
		t.Errorf("expected 'no session ID' error, got: %v", err)
	}
}

func TestArchitect_ErrorWithMissingJSONL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	mock := &architectMockCLIRunner{
		response:  "no plan header here",
		sessionID: "nonexistent-session-id-12345",
	}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)

	_, _, _, err := p.Refine(context.Background(), "prompt", PromptBrief{}, "draft")
	if err == nil {
		t.Fatal("expected error when JSONL is missing")
	}
}

func TestArchitect_PlanFileExtraction(t *testing.T) {
	sessionID := "test-plan-file-extraction"
	planMD := "# Plan\n\n## Goal\nRecovered.\n\n## Work Packages\n\n### 1. Do stuff"
	setupPlanFile(t, sessionID, planMD)

	// Mock runner returns irrelevant stdout but with session ID.
	mock := &architectMockCLIRunner{
		response:  "The plan has been saved to the plan file.",
		sessionID: sessionID,
	}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)

	plan, _, sid, err := p.Refine(context.Background(), "prompt", PromptBrief{}, "draft")
	if err != nil {
		t.Fatalf("expected plan file extraction to succeed, got: %v", err)
	}
	if plan.Markdown != planMD {
		t.Errorf("plan mismatch:\ngot:  %s\nwant: %s", plan.Markdown, planMD)
	}
	if sid != sessionID {
		t.Errorf("session ID = %q, want %q", sid, sessionID)
	}
}

func TestArchitect_RejectsOutOfBoundsPath(t *testing.T) {
	// setupPlanFile creates under ~/.claude/plans/, so we set up the evil path manually
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	sessionID := "test-oob-session"

	// Create plan file outside allowed directory
	evilFile := setupOutOfBoundsPlanFile(t, tmp, sessionID)
	_ = evilFile

	mock := &architectMockCLIRunner{
		response:  "no plan here",
		sessionID: sessionID,
	}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)

	_, _, _, err := p.Refine(context.Background(), "prompt", PromptBrief{}, "draft")
	if err == nil {
		t.Fatal("expected error for out-of-bounds plan file path")
	}
}

// setupOutOfBoundsPlanFile creates a plan file outside ~/.claude/plans/ and a JSONL pointing to it.
func setupOutOfBoundsPlanFile(t *testing.T, home, sessionID string) string {
	t.Helper()

	evilDir := filepath.Join(home, "evil")
	if err := os.MkdirAll(evilDir, 0o755); err != nil {
		t.Fatal(err)
	}
	evilFile := filepath.Join(evilDir, "stolen.md")
	if err := os.WriteFile(evilFile, []byte("# Plan\n\n## Work Packages\n..."), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(home, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	jsonlContent := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, evilFile)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return evilFile
}
