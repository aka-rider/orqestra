package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/testutil"
)

func TestArchitect_Refine_Success(t *testing.T) {
	planMD := "# Plan\n\n## Goal\nBuild a thing.\n\n## Work Packages\n\n### 1. Do stuff\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"
	sessionID := "test-refine-success"
	setupPlanFile(t, sessionID, planMD)

	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "plan saved", SessionID: sessionID}}}
	cfg := config.ArchitectConfig{
		Model:        "test-model",
		SystemPrompt: "You are the architect.",
	}

	p := NewArchitect(mock, cfg)
	plan, _, sid, err := p.Refine(context.Background(), "user prompt", "some researcher draft")
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

	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "done", SessionID: sessionID}}}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)
	_, _, _, err := p.Refine(context.Background(), "prompt", "draft")
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("expected error mentioning empty plan block, got: %v", err)
	}
}

func TestArchitect_Refine_CLIError(t *testing.T) {
	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Err: fmt.Errorf("connection refused")}}}

	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)
	_, _, _, err := p.Refine(context.Background(), "prompt", "draft")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected propagation of 'connection refused', got %v", err)
	}
}

func TestArchitect_RefineWithComments(t *testing.T) {
	planMD := "# Plan\n\n## Goal\nRevised.\n\n## Work Packages\n\n### 1. Fixed"
	sessionID := "test-refine-comments"
	setupPlanFile(t, sessionID, planMD)

	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "revised", SessionID: sessionID}}}
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
	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "no plan header here"}}}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)

	_, _, _, err := p.Refine(context.Background(), "prompt", "draft")
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

	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "no plan header here", SessionID: "nonexistent-session-id-12345"}}}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)

	_, _, _, err := p.Refine(context.Background(), "prompt", "draft")
	if err == nil || !strings.Contains(err.Error(), "no such file or directory") {
		t.Fatalf("expected no such file error for missing JSONL, got %v", err)
	}
}

func TestArchitect_PlanFileExtraction(t *testing.T) {
	sessionID := "test-plan-file-extraction"
	planMD := "# Plan\n\n## Goal\nRecovered.\n\n## Work Packages\n\n### 1. Do stuff"
	setupPlanFile(t, sessionID, planMD)

	// Mock runner returns irrelevant stdout but with session ID.
	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "The plan has been saved to the plan file.", SessionID: sessionID}}}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)

	plan, _, sid, err := p.Refine(context.Background(), "prompt", "draft")
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

	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "no plan here", SessionID: sessionID}}}
	cfg := config.ArchitectConfig{Model: "test"}
	p := NewArchitect(mock, cfg)

	_, _, _, err := p.Refine(context.Background(), "prompt", "draft")
	if err == nil || !strings.Contains(err.Error(), "is outside allowed directory") {
		t.Fatalf("expected out of bounds error, got %v", err)
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

func TestArchitect_ContinueSession_UserEditPreserved(t *testing.T) {
	// Bug scenario: user edits plan via ^E (currentPlan has edits), but the plan
	// file in ~/.claude/plans/ still has the original content. The architect gives
	// a chat-only response (doesn't update its plan file). Previously, the stale
	// plan file was returned as a "revision," silently overwriting user edits.
	// With the baseline fix, the plan file is compared against its pre-run state
	// (unchanged), so no false revision is detected.

	originalPlan := "# Plan\n\n## Goal\nOriginal.\n\n## Work Packages\n\n### 1. Do stuff\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"
	userEditedPlan := "# Plan\n\n## Goal\nUser's improved version.\n\n## Work Packages\n\n### 1. Do stuff better\n\n**Steps:**\n1. Edit foo.go and bar.go\n\n**Done when:**\n- Tests pass"

	sessionID := "test-continue-user-edit"
	setupPlanFile(t, sessionID, originalPlan)

	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{
		// ContinueSession calls RunContinue; architect gives a chat-only response
		{Output: "Looks good, I see your changes.", SessionID: sessionID},
	}}
	cfg := config.ArchitectConfig{Model: "test"}
	arch := NewArchitect(mock, cfg)

	chatResp, revisedPlan, _, err := arch.ContinueSession(
		context.Background(), sessionID, userEditedPlan, "please review the updated plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if revisedPlan != nil {
		t.Fatalf("expected no revision (user edits should be preserved), got plan: %s", revisedPlan.Markdown)
	}
	if chatResp == "" {
		t.Error("expected non-empty chat response")
	}
}

func TestArchitect_ContinueSession_ArchitectActuallyRevised(t *testing.T) {
	// The architect genuinely revises the plan during continuation.
	// The post-run plan file differs from the pre-run baseline, so
	// the revision should be detected and returned.

	originalPlan := "# Plan\n\n## Goal\nOriginal.\n\n## Work Packages\n\n### 1. Do stuff"
	revisedPlanContent := "# Plan\n\n## Goal\nRevised by architect.\n\n## Work Packages\n\n### 1. Do stuff better\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

	sessionID := "test-continue-real-revision"
	// Set up the original plan file (this is the baseline).
	planFile := setupPlanFile(t, sessionID, originalPlan)

	// Create a second plan file that the post-run result will point to,
	// simulating the architect writing a new version during the run.
	home := os.Getenv("HOME")
	revisedFile := filepath.Join(home, ".claude", "plans", sessionID+"-revised-plan.md")
	if err := os.WriteFile(revisedFile, []byte(revisedPlanContent), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = planFile

	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{
		// RunContinue result points to the revised plan file via PlanFilePath.
		// Baseline read resolves via JSONL → original file.
		// Post-run read uses PlanFilePath → revised file.
		{Output: "I've updated the plan.", SessionID: sessionID, PlanFilePath: revisedFile},
	}}
	cfg := config.ArchitectConfig{Model: "test"}
	arch := NewArchitect(mock, cfg)

	_, revisedPlan, _, err := arch.ContinueSession(
		context.Background(), sessionID, originalPlan, "please add error handling", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if revisedPlan == nil {
		t.Fatal("expected a revised plan, got nil")
	}
	if revisedPlan.Markdown != strings.TrimSpace(revisedPlanContent) {
		t.Errorf("revised plan content mismatch:\ngot:  %s\nwant: %s", revisedPlan.Markdown, revisedPlanContent)
	}
}

func TestArchitect_ContinueWithCriticReport_BaselineComparison(t *testing.T) {
	// Same baseline comparison applies to critic continuation.
	originalPlan := "# Plan\n\n## Goal\nOriginal.\n\n## Work Packages\n\n### 1. Do stuff"
	sessionID := "test-critic-continue-baseline"
	setupPlanFile(t, sessionID, originalPlan)

	mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{
		{Output: "All findings addressed.", SessionID: sessionID},
	}}
	cfg := config.ArchitectConfig{Model: "test"}
	arch := NewArchitect(mock, cfg)

	// Pass a user-edited currentPlan that differs from the plan file.
	userEditedPlan := "# Plan\n\n## Goal\nUser edited.\n\n## Work Packages\n\n### 1. Edited"
	_, revisedPlan, _, err := arch.ContinueWithCriticReport(
		context.Background(), sessionID, userEditedPlan, "## Critic Report\n\nNo blockers.", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if revisedPlan != nil {
		t.Fatalf("expected no revision (plan file unchanged), got: %s", revisedPlan.Markdown)
	}
}
