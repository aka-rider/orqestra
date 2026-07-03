package orchestrator

// INV-P3-BRANCH: branch detection is a pre-token-spend boundary and must fail
// closed. If worktree.CurrentBranch cannot determine the current branch (e.g.
// detached HEAD, non-git repo path), ExecuteStep must surface the failure —
// call Observer.AgentFailed and return an error — never silently continue and
// run the worker against the live repo with no isolation (J8).

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/rundir"
)

func TestExecuteStep_BranchDetectFailure_ReturnsError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	// A plain (non-git) directory makes worktree.CurrentBranch fail — this
	// reproduces J8's detached-HEAD/odd-repo-state class of failure without
	// needing a real detached-HEAD checkout.
	repoDir := t.TempDir()

	obs := newRecordingObserver()
	step := &ExecuteStep{
		Spec:     newReplaySpec("# Plan\nDo work."),
		RepoPath: repoDir,
	}

	sc := StepContext{
		Exec:      &fixturePlayer{path: "../harness/testdata/worker_stream_sample.jsonl"},
		Obs:       obs,
		Artifacts: NoopArtifactSink(),
		Sessions:  rundir.Dir{Path: t.TempDir()},
		Log:       slog.Default(),
		RepoPath:  repoDir,
	}

	_, err := step.Run(context.Background(), ExecuteInput{
		RunID:     "branch-detect-gate",
		FinalPlan: "# Plan\nDo work.",
	}, sc)

	// Fail closed: branch-detection failure must surface as an error...
	if err == nil {
		t.Fatal("INV-P3-BRANCH: branch-detection failure was swallowed — Run returned nil " +
			"(worker would run against the live, unisolated repo, J8)")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Errorf("INV-P3-BRANCH: error should name the branch-detection failure, got: %v", err)
	}

	// ...and as an observable worker failure event, never a bare warning log.
	if !obs.Failed("worker") {
		t.Error("INV-P3-BRANCH: Observer.AgentFailed was not called for the worker — " +
			"the TUI/pipeline never learns branch detection failed")
	}
}
