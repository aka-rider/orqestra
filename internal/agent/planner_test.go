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

// plannerMockCLIRunner is a test double for the CLIRunner interface.
type plannerMockCLIRunner struct {
	response  string
	sessionID string
	err       error
}

func (m *plannerMockCLIRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response, SessionID: m.sessionID}, nil
}

func (m *plannerMockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response, SessionID: m.sessionID}, nil
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

func TestPlanner_RecoverySkippedWithoutSessionID(t *testing.T) {
	// Output that fails parsePlanResult, no session ID → original error returned.
	mock := &plannerMockCLIRunner{response: "no plan header here"}
	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)

	_, _, _, err := p.Refine(context.Background(), "draft")
	if err == nil {
		t.Fatal("expected error for missing '# Plan' header")
	}
	if !strings.Contains(err.Error(), "# Plan") {
		t.Errorf("expected original parse error, got: %v", err)
	}
}

func TestPlanner_RecoveryFailsGracefully(t *testing.T) {
	// Output that fails parsePlanResult, session ID present but no matching session log.
	mock := &plannerMockCLIRunner{
		response:  "no plan header here",
		sessionID: "nonexistent-session-id-12345",
	}
	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)

	_, _, _, err := p.Refine(context.Background(), "draft")
	if err == nil {
		t.Fatal("expected error when recovery fails")
	}
	// Should return the original parse error, not the recovery error.
	if !strings.Contains(err.Error(), "# Plan") {
		t.Errorf("expected original parse error returned, got: %v", err)
	}
}

func TestPlanner_RecoverySuccess(t *testing.T) {
	// Set up temp filesystem mimicking ~/.claude/ structure.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	sessionID := "test-recovery-session"
	planMD := "# Plan\n\n## Goal\nRecovered.\n\n## Work Packages\n\n### 1. Do stuff"

	// Create plan file under ~/.claude/plans/
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(plansDir, "recovered-plan.md")
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

	// Mock runner returns bad output but with a session ID.
	mock := &plannerMockCLIRunner{
		response:  "The plan has been saved to the plan file.",
		sessionID: sessionID,
	}
	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)

	plan, _, sid, err := p.Refine(context.Background(), "draft")
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

func TestPlanner_RecoveryRejectsOutOfBoundsPath(t *testing.T) {
	// Set up temp filesystem with a plan file outside ~/.claude/plans/.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	sessionID := "test-oob-session"

	// Create a plan file outside the allowed directory.
	evilDir := filepath.Join(tmp, "evil")
	if err := os.MkdirAll(evilDir, 0o755); err != nil {
		t.Fatal(err)
	}
	evilPlanFile := filepath.Join(evilDir, "stolen.md")
	if err := os.WriteFile(evilPlanFile, []byte("# Plan\n\n## Work Packages\n..."), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create session JSONL pointing to the evil path.
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
	jsonlContent := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, evilPlanFile)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &plannerMockCLIRunner{
		response:  "no plan here",
		sessionID: sessionID,
	}
	cfg := config.PlannerConfig{Model: "test"}
	p := NewPlanner(mock, cfg)

	_, _, _, err = p.Refine(context.Background(), "draft")
	if err == nil {
		t.Fatal("expected error for out-of-bounds plan file path")
	}
	// Should return original parse error (recovery failed).
	if !strings.Contains(err.Error(), "# Plan") {
		t.Errorf("expected original parse error, got: %v", err)
	}
}
