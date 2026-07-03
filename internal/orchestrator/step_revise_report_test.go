package orchestrator

// J35 (RC3/WP11): a revision turn that did not rewrite the plan file must
// NOT return the stale pre-revision plan-file content as if it were the
// fresh revision — the console/final-message response (when it passes the
// looksLikeReport sanity check) must win instead. Before the WP11 fix,
// ReviseStep unconditionally preferred the plan file whenever it was
// readable, with no notion of "did this invocation actually change it".

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// writeRevisionPlanFile creates <tmp>/.claude/plans/<name>.md under a fresh
// HOME so agent.ReadPlanFile's security gate accepts it, and returns the
// absolute path. Mirrors internal/agent/plan_extract_test.go's setupPlanFile
// helper (kept local — orchestrator cannot import agent's unexported test
// helper, and the fixture shape needed here is simpler: one direct path, no
// JSONL attachment).
func writeRevisionPlanFile(t *testing.T, name, content string) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(plansDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReviseStep_UnchangedPlanFile_FreshFinalMessageWins is the RED-first
// gate for J35 on the human-gate revise path. The architect's revision turn
// resumes the same session and reports the SAME plan file path it always
// has (res.PlanFilePath == in.Plan.PlanFilePath) — the file was never
// rewritten this turn — while the turn's final console message carries a
// fresh, valid revision. ReviseStep must return the fresh message, not the
// stale file.
func TestReviseStep_UnchangedPlanFile_FreshFinalMessageWins(t *testing.T) {
	stalePlan := "# Plan\n\n## Goal\nOriginal pre-revision plan.\n\n## Work Packages\n### 1. Do X\n"
	planFile := writeRevisionPlanFile(t, "revise-unchanged-plan.md", stalePlan)

	freshFinalMessage := "# Plan\n\n## Goal\nRevised plan incorporating feedback.\n\n" +
		"## Work Packages\n### 1. Do X\n### 2. Do Y (new, from critic feedback)"

	exec := &sequencedExecutor{
		results: []harness.RunResult{{
			SessionID:    "revise-test-session",
			PlanFilePath: planFile, // same path as before — the file was NOT rewritten this turn
			Output:       freshFinalMessage,
		}},
		errs: []error{nil},
	}

	step := &ReviseStep{ArchSpec: harness.ProcessSpec{AgentID: "architect", PlanMode: true}}
	sc := StepContext{
		Exec:      exec,
		Obs:       newRecordingObserver(),
		Artifacts: NoopArtifactSink(),
		Log:       slog.Default(),
		RepoPath:  t.TempDir(),
	}

	in := ReviseInput{
		Plan: PlanOutput{
			Markdown:     stalePlan,
			SessionID:    "revise-test-session",
			PlanFilePath: planFile, // the harvester's ONLY way to know the file's pre-invocation state
		},
		Decision: Decision{Type: DecisionComment, Comment: "please add step 2"},
	}

	out, err := step.Run(context.Background(), in, sc)
	if err != nil {
		t.Fatalf("ReviseStep.Run: unexpected error: %v", err)
	}
	if out.Markdown != freshFinalMessage {
		t.Errorf("J35: ReviseStep returned the STALE plan-file content instead of the fresh final message.\ngot:  %q\nwant: %q",
			out.Markdown, freshFinalMessage)
	}
}

// TestReviseStep_ChangedPlanFile_TierTwoWins is the companion gate: when the
// plan file DID change during the invocation (different mtime/size from the
// pre-invocation snapshot), tier 2 (plan file) must still win over tier 3 —
// the file is the authoritative, tool-verified plan; the final message is
// only a fallback probe.
func TestReviseStep_ChangedPlanFile_TierTwoWins(t *testing.T) {
	stalePlan := "# Plan\n\n## Goal\nOriginal pre-revision plan.\n\n## Work Packages\n### 1. Do X\n"
	planFile := writeRevisionPlanFile(t, "revise-changed-plan.md", stalePlan)

	revisedPlan := "# Plan\n\n## Goal\nRevised plan, rewritten by the architect this turn.\n\n" +
		"## Work Packages\n### 1. Do X\n### 2. Do Y"

	exec := &rewritingExecutor{
		path:    planFile,
		content: revisedPlan,
		result: harness.RunResult{
			SessionID:    "revise-test-session-2",
			PlanFilePath: planFile,
			Output:       "Done — see the updated plan.", // short chat text, fails looksLikeReport
		},
	}

	step := &ReviseStep{ArchSpec: harness.ProcessSpec{AgentID: "architect", PlanMode: true}}
	sc := StepContext{
		Exec:      exec,
		Obs:       newRecordingObserver(),
		Artifacts: NoopArtifactSink(),
		Log:       slog.Default(),
		RepoPath:  t.TempDir(),
	}

	in := ReviseInput{
		Plan: PlanOutput{
			Markdown:     stalePlan,
			SessionID:    "revise-test-session-2",
			PlanFilePath: planFile,
		},
		Decision: Decision{Type: DecisionComment, Comment: "please add step 2"},
	}

	out, err := step.Run(context.Background(), in, sc)
	if err != nil {
		t.Fatalf("ReviseStep.Run: unexpected error: %v", err)
	}
	if out.Markdown != revisedPlan {
		t.Errorf("expected the rewritten plan file to win tier 2.\ngot:  %q\nwant: %q", out.Markdown, revisedPlan)
	}
}

// rewritingExecutor simulates an agent invocation that REWRITES the plan
// file at path (new content, new mtime/size) before returning result — used
// to prove the "changed" branch of the J35 freshness check.
type rewritingExecutor struct {
	path    string
	content string
	result  harness.RunResult
}

func (e *rewritingExecutor) Run(_ context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
	if err := os.WriteFile(e.path, []byte(e.content), 0o644); err != nil {
		return harness.RunResult{}, err
	}
	return e.result, nil
}
