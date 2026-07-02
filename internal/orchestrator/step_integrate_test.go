package orchestrator

// INV-P3-INTEGRATE: truthful integrate. A non-empty Worktree.Path means isolated
// work exists; if the target branch to merge it into is unknown, IntegrateStep
// must fail — never report StatusSuccess with nothing committed (J9). An empty
// Worktree.Path (isolation never requested — e.g. WorktreeSpecFn nil) is the one
// honest no-op: nothing was isolated, so there is nothing to merge.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/xiii/orqestra/internal/worktree"
)

func TestIntegrateStep_NonEmptyWorktree_EmptyTargetBranch_Errors(t *testing.T) {
	step := &IntegrateStep{}
	sc := StepContext{
		Artifacts: NoopArtifactSink(),
		Log:       slog.Default(),
	}

	out, err := step.Run(context.Background(), IntegrateInput{
		Worktree:     worktree.Worktree{Path: "/tmp/some-worktree-path", RepoPath: "/tmp/repo", Branch: "orqestra-run-x"},
		RunID:        "int-gate",
		TargetBranch: "",
	}, sc)

	if err == nil {
		t.Fatalf("INV-P3-INTEGRATE: empty target branch with a non-empty worktree path was "+
			"reported as success (status=%q) — commits are stranded on the worktree branch with "+
			"nothing merged into the user's repo (J9)", out.Status)
	}
	if out.Status == StatusSuccess {
		t.Errorf("INV-P3-INTEGRATE: expected a non-success status alongside the error, got %q", out.Status)
	}
}

func TestIntegrateStep_EmptyWorktree_StaysHonestNoop(t *testing.T) {
	// No worktree was ever created (isolation legitimately absent) — this is the
	// ONE case allowed to no-op with StatusSuccess, since nothing was isolated
	// and therefore nothing needs merging.
	step := &IntegrateStep{}
	sc := StepContext{
		Artifacts: NoopArtifactSink(),
		Log:       slog.Default(),
	}

	out, err := step.Run(context.Background(), IntegrateInput{
		Worktree:     worktree.Worktree{},
		RunID:        "int-gate-noop",
		TargetBranch: "",
	}, sc)

	if err != nil {
		t.Fatalf("expected no error when no worktree was ever created, got: %v", err)
	}
	if out.Status != StatusSuccess {
		t.Errorf("expected StatusSuccess for the isolation-absent no-op, got %q", out.Status)
	}
}

// fakeIntegrateStep returns a step that emits a fixed IntegrateOutput, mimicking
// IntegrateStep.handleConflict's give-up path (Status: StatusFailed, ConflictFiles
// populated, err == nil — see step_integrate.go's preserve() closure).
func fakeIntegrateStep(out IntegrateOutput) Step[IntegrateInput, IntegrateOutput] {
	return &fakeStep[IntegrateInput, IntegrateOutput]{
		agentID: "integrator",
		fn: func(ctx context.Context, in IntegrateInput, sc StepContext) (IntegrateOutput, error) {
			return out, nil
		},
	}
}

// TestRunPipeline_ConflictGiveUp_PopulatesResultConflictFiles is the WP3 J10
// end-to-end gate: on conflict give-up, RunPipeline's Result must carry the
// conflict file list (previously dropped — see the dead MergeConflictInfo type,
// events.go, and run_pipeline.go which kept only .Status).
func TestRunPipeline_ConflictGiveUp_PopulatesResultConflictFiles(t *testing.T) {
	steps := defaultTestSteps()
	wantConflicts := []string{"internal/foo.go", "internal/bar.go"}
	steps.Integrate = fakeIntegrateStep(IntegrateOutput{
		Status:        StatusFailed,
		ConflictFiles: wantConflicts,
	})

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: nil,
	}

	result, err := runPipelineSync(context.Background(), setup, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, StatusFailed)
	}
	if len(result.ConflictFiles) == 0 {
		t.Fatal("INV-P3-CONFLICT: Result.ConflictFiles is empty on conflict give-up — " +
			"the user cannot see which files conflicted (J10)")
	}
	if len(result.ConflictFiles) != len(wantConflicts) {
		t.Fatalf("Result.ConflictFiles = %v, want %v", result.ConflictFiles, wantConflicts)
	}
	for i, f := range wantConflicts {
		if result.ConflictFiles[i] != f {
			t.Errorf("Result.ConflictFiles[%d] = %q, want %q", i, result.ConflictFiles[i], f)
		}
	}
}
