package orchestrator

// WP8/J33: worker self-validation's parsed verdict used to be computed by
// ValidateStep and then silently discarded — run_pipeline.go read only
// .Output, never .Parsed, so a FAILED self-validation still proceeded to an
// unconditional commit + merge. This file gates the optional
// PipelineSetup.BlockMergeOnValidationFail safety valve: when enabled and the
// verdict is agent.VerdictFail, Integrate must never run, and the run must
// fail with an explicit, inspectable reason instead of merging silently.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/worktree"
)

// TestRunPipeline_BlockMergeOnValidationFail_SkipsIntegrate is the WP8/J33
// gate (b): with the flag enabled and a FAIL verdict, Integrate must NOT be
// invoked, the run must report StatusFailed, and the returned error must name
// the reason. The worktree is left exactly as Execute produced it (Integrate
// is the only step that merges, commits, or removes it) — recoverable per
// CLAUDE.md §0.
func TestRunPipeline_BlockMergeOnValidationFail_SkipsIntegrate(t *testing.T) {
	steps := defaultTestSteps()
	steps.Validate = fakeValidateStep(agent.MarkerFail + " tests failed")

	integrateInvoked := false
	steps.Integrate = &fakeStep[IntegrateInput, IntegrateOutput]{
		agentID: "integrator",
		fn: func(ctx context.Context, in IntegrateInput, sc StepContext) (IntegrateOutput, error) {
			integrateInvoked = true
			return IntegrateOutput{Status: StatusSuccess}, nil
		},
	}

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates:                 nil,
		BlockMergeOnValidationFail: true,
	}

	result, err := runPipelineSync(context.Background(), setup, steps)

	if integrateInvoked {
		t.Error("BlockMergeOnValidationFail: Integrate was invoked despite a FAIL verdict — merge was not blocked")
	}
	if err == nil {
		t.Fatal("BlockMergeOnValidationFail: expected an explicit error when blocking merge on a FAIL verdict, got nil")
	}
	if !strings.Contains(err.Error(), "block_merge_on_validation_fail") {
		t.Errorf("error %q does not name the block_merge_on_validation_fail reason", err.Error())
	}
	if result.Status != StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, StatusFailed)
	}
	if result.ValidationVerdict != agent.VerdictFail {
		t.Errorf("ValidationVerdict = %q, want %q", result.ValidationVerdict, agent.VerdictFail)
	}
}

// TestRunPipeline_BlockMergeOnValidationFail_WorktreePreserved additionally
// proves the worktree itself is never touched (no merge, no removal) when the
// gate fires — Execute's worktree.Path stays valid and nothing in RunPipeline
// calls Remove/MergeInto on it, because Integrate never runs.
func TestRunPipeline_BlockMergeOnValidationFail_WorktreePreserved(t *testing.T) {
	wt := worktree.Worktree{Path: "/tmp/orqestra-preserved-wt", RepoPath: "/tmp/repo", Branch: "orqestra-run-preserved"}

	steps := defaultTestSteps()
	steps.Validate = fakeValidateStep(agent.MarkerFail + " tests failed")
	steps.Execute = &fakeStep[ExecuteInput, ExecuteOutput]{
		agentID: "worker",
		fn: func(ctx context.Context, in ExecuteInput, sc StepContext) (ExecuteOutput, error) {
			return ExecuteOutput{WorkOutput: "done", Worktree: wt, TargetBranch: "main"}, nil
		},
	}
	steps.Integrate = &fakeStep[IntegrateInput, IntegrateOutput]{
		agentID: "integrator",
		fn: func(ctx context.Context, in IntegrateInput, sc StepContext) (IntegrateOutput, error) {
			t.Fatal("Integrate must not run when BlockMergeOnValidationFail blocks a FAIL verdict")
			return IntegrateOutput{}, errors.New("unreachable")
		},
	}

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates:                 nil,
		BlockMergeOnValidationFail: true,
	}

	result, err := runPipelineSync(context.Background(), setup, steps)
	if err == nil {
		t.Fatal("expected an error when blocking merge on a FAIL verdict")
	}
	if result.Status != StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, StatusFailed)
	}
	// The worktree path must be named in the error so the user/operator can
	// find and recover it — never silently dropped.
	if !strings.Contains(err.Error(), wt.Path) {
		t.Errorf("error %q does not name the preserved worktree path %q", err.Error(), wt.Path)
	}
}
