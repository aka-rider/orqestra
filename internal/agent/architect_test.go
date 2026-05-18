package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/testutil"
)

const (
	archPlanA = "# Plan\n\n## Goal\nOriginal.\n\n## Work Packages\n\n### 1. Do stuff\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"
	archPlanB = "# Plan\n\n## Goal\nUser improved.\n\n## Work Packages\n\n### 1. Better stuff\n\n**Steps:**\n1. Edit foo.go and bar.go\n\n**Done when:**\n- Tests pass"
	archPlanC = "# Plan\n\n## Goal\nArchitect revision.\n\n## Work Packages\n\n### 1. New approach\n\n**Steps:**\n1. Rewrite baz.go\n\n**Done when:**\n- All tests pass"
	archPlanD = "# Plan\n\n## Goal\nBuilds on user edit.\n\n## Work Packages\n\n### 1. Extended stuff\n\n**Steps:**\n1. Edit foo.go, bar.go, baz.go\n\n**Done when:**\n- Integration tests pass"
)

func writePlanFile(t *testing.T, sessionID, content string) {
	t.Helper()
	home := os.Getenv("HOME")
	planPath := filepath.Join(home, ".claude", "plans", sessionID+"-plan.md")
	if err := os.WriteFile(planPath, []byte(content), 0o644); err != nil {
		t.Fatalf("writePlanFile: %v", err)
	}
}

func TestPlanner_Continue_RevisionDetection(t *testing.T) {
	tests := []struct {
		name        string
		initialPlan string // content in plan file before the run (baseline)
		currentPlan string // what the user/TUI considers the current plan
		postRunPlan string // plan file content AFTER RunContinue ("" = unchanged)
		wantRevised bool
		wantContent string
	}{
		{
			name:        "chat-only: plan file unchanged",
			initialPlan: archPlanA,
			currentPlan: archPlanA,
			postRunPlan: "", // RunContinue doesn't touch the plan file
			wantRevised: false,
		},
		{
			name:        "user edited via ^E, architect gives chat-only response",
			initialPlan: archPlanA,
			currentPlan: archPlanB, // user edited
			postRunPlan: "",        // architect didn't write to plan file
			wantRevised: false,
		},
		{
			name:        "echo suppression: architect copies user edits into plan file",
			initialPlan: archPlanA,
			currentPlan: archPlanB,
			postRunPlan: archPlanB, // architect wrote B, but user already has B
			wantRevised: false,
		},
		{
			name:        "real revision: architect writes new content",
			initialPlan: archPlanA,
			currentPlan: archPlanA,
			postRunPlan: archPlanC,
			wantRevised: true,
			wantContent: strings.TrimSpace(archPlanC),
		},
		{
			name:        "revision after user edit: architect builds on user changes",
			initialPlan: archPlanA,
			currentPlan: archPlanB,
			postRunPlan: archPlanD, // different from both baseline A and user's B
			wantRevised: true,
			wantContent: strings.TrimSpace(archPlanD),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.MustTempHome(t)
			sessionID := "arch-cont-" + strings.ReplaceAll(tt.name, " ", "-")

			// Set up initial plan file (and JSONL for ReadPlanFromRun).
			setupPlanFile(t, sessionID, tt.initialPlan)

			// Read baseline BEFORE the continuation (as the orchestrator does).
			baseline, baselineErr := ReadPlanFromRun(harness.RunResult{SessionID: sessionID})

			// FakeRunner simulates the architect writing a new plan file during RunContinue.
			var calls []testutil.FakeCall
			if tt.postRunPlan != "" {
				planContent := tt.postRunPlan
				calls = []testutil.FakeCall{{
					Output:    "architect response",
					SessionID: sessionID,
					OnCall: func(idx int) {
						writePlanFile(t, sessionID, planContent)
					},
				}}
			} else {
				// No OnCall: plan file stays unchanged.
				calls = []testutil.FakeCall{{Output: "architect response", SessionID: sessionID}}
			}

			planner := NewPlanner(&testutil.FakeRunner{Calls: calls}, "system-prompt")
			result, err := planner.Continue(context.Background(), sessionID, "review comment", nil)
			if err != nil {
				t.Fatalf("Continue: %v", err)
			}

			// Apply revision detection as the orchestrator would.
			revisedPlan := DetectPlanRevision(result.Plan, baseline, baselineErr, tt.currentPlan)

			if tt.wantRevised && revisedPlan == nil {
				t.Error("expected revisedPlan != nil, got nil")
			}
			if !tt.wantRevised && revisedPlan != nil {
				t.Errorf("expected revisedPlan == nil, got %+v", revisedPlan)
			}
			if tt.wantRevised && revisedPlan != nil && revisedPlan.Markdown != tt.wantContent {
				t.Errorf("plan content mismatch:\ngot:  %q\nwant: %q",
					revisedPlan.Markdown, tt.wantContent)
			}
		})
	}
}

func TestPlanner_Continue_ChatAlwaysPopulated(t *testing.T) {
	testutil.MustTempHome(t)
	sessionID := "arch-chat-test"
	setupPlanFile(t, sessionID, archPlanA)

	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{
		{Output: "here is my answer", SessionID: sessionID},
	}}
	planner := NewPlanner(runner, "sys")
	result, err := planner.Continue(context.Background(), sessionID, "what is X?", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Chat != "here is my answer" {
		t.Errorf("Chat = %q, want %q", result.Chat, "here is my answer")
	}
}
