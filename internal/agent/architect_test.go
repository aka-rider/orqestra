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

// ---------------------------------------------------------------------------
// Contract: ContinueSession revision detection
//
// Spec: revisedPlan != nil ⟺ the architect produced content the user doesn't
// already have. Specifically, two conditions must hold:
//   1. The plan file changed from its pre-run baseline (architect wrote).
//   2. The new content differs from currentPlan (not echoing user edits).
// ---------------------------------------------------------------------------

func TestArchitect_ContinueSession_RevisionDetection(t *testing.T) {
	const (
		planA = "# Plan\n\n## Goal\nOriginal.\n\n## Work Packages\n\n### 1. Do stuff\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"
		planB = "# Plan\n\n## Goal\nUser improved.\n\n## Work Packages\n\n### 1. Better stuff\n\n**Steps:**\n1. Edit foo.go and bar.go\n\n**Done when:**\n- Tests pass"
		planC = "# Plan\n\n## Goal\nArchitect revision.\n\n## Work Packages\n\n### 1. New approach\n\n**Steps:**\n1. Rewrite baz.go\n\n**Done when:**\n- All tests pass"
		planD = "# Plan\n\n## Goal\nBuilds on user edit.\n\n## Work Packages\n\n### 1. Extended stuff\n\n**Steps:**\n1. Edit foo.go, bar.go, baz.go\n\n**Done when:**\n- Integration tests pass"
	)

	tests := []struct {
		name        string
		planFile    string // content written to ~/.claude/plans/ before the run
		currentPlan string // what the user/TUI considers the current plan
		postRunFile string // content the architect writes during the run ("" = unchanged)
		wantRevised bool   // whether revisedPlan should be non-nil
		wantContent string // if wantRevised, the expected markdown content
	}{
		{
			name:        "chat-only response, plan file unchanged",
			planFile:    planA,
			currentPlan: planA,
			postRunFile: "",    // architect didn't touch the plan file
			wantRevised: false,
		},
		{
			name:        "user edited via ^E, architect gives chat-only response",
			planFile:    planA,
			currentPlan: planB, // user edited, but plan file still has A
			postRunFile: "",    // architect didn't touch the plan file
			wantRevised: false, // plan file unchanged from baseline → no revision
		},
		{
			name:        "echo suppression: architect copies user edits into plan file",
			planFile:    planA,
			currentPlan: planB,
			postRunFile: planB, // architect wrote B, but user already has B
			wantRevised: false, // echo: new content == currentPlan → suppressed
		},
		{
			name:        "real revision: architect writes new content",
			planFile:    planA,
			currentPlan: planA,
			postRunFile: planC, // genuinely new content
			wantRevised: true,
			wantContent: strings.TrimSpace(planC),
		},
		{
			name:        "revision after user edit: architect builds on user changes",
			planFile:    planA,
			currentPlan: planB,
			postRunFile: planD, // different from both baseline A and user's B
			wantRevised: true,
			wantContent: strings.TrimSpace(planD),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := "test-continue-" + strings.ReplaceAll(tt.name, " ", "-")
			setupPlanFile(t, sessionID, tt.planFile)

			call := testutil.FakeCall{
				Output:    "Architect response.",
				SessionID: sessionID,
			}

			// If the architect writes new content, create a separate plan file
			// and point RunResult.PlanFilePath at it (simulates plan file update).
			if tt.postRunFile != "" {
				home := os.Getenv("HOME")
				revised := filepath.Join(home, ".claude", "plans", sessionID+"-revised.md")
				if err := os.WriteFile(revised, []byte(tt.postRunFile), 0o644); err != nil {
					t.Fatal(err)
				}
				call.PlanFilePath = revised
			}

			mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{call}}
			arch := NewArchitect(mock, config.ArchitectConfig{Model: "test"})

			chatResp, revisedPlan, _, err := arch.ContinueSession(
				context.Background(), sessionID, tt.currentPlan, "review", nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if chatResp == "" {
				t.Error("expected non-empty chat response")
			}

			if tt.wantRevised {
				if revisedPlan == nil {
					t.Fatal("expected a revised plan, got nil")
				}
				if revisedPlan.Markdown != tt.wantContent {
					t.Errorf("revised plan content mismatch:\ngot:  %s\nwant: %s", revisedPlan.Markdown, tt.wantContent)
				}
			} else {
				if revisedPlan != nil {
					t.Fatalf("expected no revision, got plan:\n%s", revisedPlan.Markdown)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Contract: ContinueWithCriticReport revision detection
//
// Same two-condition spec as ContinueSession. The critic prompt differs,
// but the revision detection contract is identical.
// ---------------------------------------------------------------------------

func TestArchitect_ContinueWithCriticReport_RevisionDetection(t *testing.T) {
	const (
		planA = "# Plan\n\n## Goal\nOriginal.\n\n## Work Packages\n\n### 1. Do stuff\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"
		planB = "# Plan\n\n## Goal\nUser edited.\n\n## Work Packages\n\n### 1. Edited stuff"
		planC = "# Plan\n\n## Goal\nCritic-revised.\n\n## Work Packages\n\n### 1. Fixed per critic\n\n**Steps:**\n1. Fix foo.go\n\n**Done when:**\n- Critic satisfied"
	)

	tests := []struct {
		name        string
		planFile    string
		currentPlan string
		postRunFile string
		wantRevised bool
		wantContent string
	}{
		{
			name:        "critic: chat-only, plan file unchanged",
			planFile:    planA,
			currentPlan: planA,
			postRunFile: "",
			wantRevised: false,
		},
		{
			name:        "critic: user edited, plan file unchanged",
			planFile:    planA,
			currentPlan: planB,
			postRunFile: "",
			wantRevised: false,
		},
		{
			name:        "critic: architect revises plan based on critic findings",
			planFile:    planA,
			currentPlan: planA,
			postRunFile: planC,
			wantRevised: true,
			wantContent: strings.TrimSpace(planC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := "test-critic-" + strings.ReplaceAll(tt.name, " ", "-")
			setupPlanFile(t, sessionID, tt.planFile)

			call := testutil.FakeCall{
				Output:    "Addressed critic findings.",
				SessionID: sessionID,
			}
			if tt.postRunFile != "" {
				home := os.Getenv("HOME")
				revised := filepath.Join(home, ".claude", "plans", sessionID+"-revised.md")
				if err := os.WriteFile(revised, []byte(tt.postRunFile), 0o644); err != nil {
					t.Fatal(err)
				}
				call.PlanFilePath = revised
			}

			mock := &testutil.FakeRunner{Calls: []testutil.FakeCall{call}}
			arch := NewArchitect(mock, config.ArchitectConfig{Model: "test"})

			_, revisedPlan, _, err := arch.ContinueWithCriticReport(
				context.Background(), sessionID, tt.currentPlan, "## Critic Report\n\nFindings here.", nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantRevised {
				if revisedPlan == nil {
					t.Fatal("expected a revised plan, got nil")
				}
				if revisedPlan.Markdown != tt.wantContent {
					t.Errorf("revised plan content mismatch:\ngot:  %s\nwant: %s", revisedPlan.Markdown, tt.wantContent)
				}
			} else {
				if revisedPlan != nil {
					t.Fatalf("expected no revision, got plan:\n%s", revisedPlan.Markdown)
				}
			}
		})
	}
}
