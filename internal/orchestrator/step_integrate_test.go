package orchestrator

// INV-P3-INTEGRATE: truthful integrate. A non-empty Worktree.Path means isolated
// work exists; if the target branch to merge it into is unknown, IntegrateStep
// must fail — never report StatusSuccess with nothing committed (J9). An empty
// Worktree.Path (isolation never requested — e.g. WorktreeSpecFn nil) is the one
// honest no-op: nothing was isolated, so there is nothing to merge.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
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

// recordingArtifactSink is declared in step_deliberate_test.go (shared test
// double for inspecting persisted meta artifacts).

// panicExecutor fails the test immediately if Run is ever called — used to
// prove a code path never executes an agent (e.g. a zero ProcessSpec, J19).
type panicExecutor struct{ t *testing.T }

func (p panicExecutor) Run(_ context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
	p.t.Fatal("Exec.Run must never be called when ConflictSpecFn returned an error — this would execute a zero ProcessSpec (J19)")
	return harness.RunResult{}, nil
}

// setupConflictWorktree creates a real git repo + worktree with a genuine
// merge conflict on shared.txt: the worktree branch and the target branch
// each modify it differently after the worktree was created.
func setupConflictWorktree(t *testing.T) (wt worktree.Worktree, target string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "seed")

	ctx := context.Background()
	var err error
	target, err = worktree.CurrentBranch(ctx, repo)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}

	sessionDir := filepath.Join(t.TempDir(), "session")
	wt, err = worktree.Create(ctx, repo, sessionDir, "conflict-spec-err")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Worker modifies shared.txt in the worktree and commits it.
	if err := os.WriteFile(filepath.Join(wt.Path, "shared.txt"), []byte("worker change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.StageAll(ctx); err != nil {
		t.Fatalf("StageAll: %v", err)
	}
	if err := wt.CommitStaged(ctx, "worker change"); err != nil {
		t.Fatalf("CommitStaged: %v", err)
	}

	// Drift the target branch so the merge conflicts.
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "base drift")

	return wt, target
}

// TestIntegrateStep_ConflictSpecBuildError_PreservesGiveUp is the J19 RED-first
// gate: when ConflictSpecFn fails to build a spec, handleConflict must give up
// and preserve the worktree — carrying the spec-build error in the give-up
// reason — and must NEVER call Exec.Run with the zero ProcessSpec that used to
// result from a swallowed build error (no sandbox, no model routing, empty
// AgentID).
func TestIntegrateStep_ConflictSpecBuildError_PreservesGiveUp(t *testing.T) {
	wt, target := setupConflictWorktree(t)
	defer wt.Remove(context.Background(), true) //nolint:errcheck

	specErr := errors.New("model resolution failed: boom")
	step := &IntegrateStep{
		ResolveConflicts: true,
		ConflictSpecFn: func(wtPath string) (harness.ProcessSpec, error) {
			return harness.ProcessSpec{}, specErr
		},
	}
	sink := newRecordingArtifactSink()
	sc := StepContext{
		Exec:      panicExecutor{t: t},
		Artifacts: sink,
		Log:       slog.Default(),
	}

	out, err := step.Run(context.Background(), IntegrateInput{
		Worktree:     wt,
		RunID:        "conflict-spec-err",
		TargetBranch: target,
	}, sc)

	if err != nil {
		t.Fatalf("expected nil error (give-up is reported via Status/ConflictFiles, not err): %v", err)
	}
	if out.Status != StatusFailed {
		t.Errorf("status = %q, want %q", out.Status, StatusFailed)
	}
	if len(out.ConflictFiles) == 0 {
		t.Fatal("expected non-empty ConflictFiles on give-up")
	}

	data, ok := sink.writes["integrator_meta.json"]
	if !ok {
		t.Fatal("expected integrator_meta.json to be written recording the give-up")
	}
	if !strings.Contains(string(data), specErr.Error()) {
		t.Errorf("give-up reason does not carry the spec-build error %q: %s", specErr.Error(), data)
	}
}
