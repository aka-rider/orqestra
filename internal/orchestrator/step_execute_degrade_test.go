package orchestrator

// INV-P3-DEGRADE: worktree isolation must fail closed.
//
// When isolation is requested (WorktreeSpecFn set) but worktree.Create fails,
// ExecuteStep must surface the failure — call Observer.AgentFailed and return an
// error — never silently fall back to the live repo (the DEFECT-03 failure mode,
// fixed 2026-06-22). This is the gate that replaced the DEFECT-03 canary.

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/rundir"
)

func TestExecuteStep_WorktreeFailure_EmitsEvent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	// Real git repo — required so CurrentBranch succeeds and the worktree guard
	// is actually entered.
	repoDir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed (repo setup): %v\n%s", args, err, out)
		}
	}

	// Place a regular file where worktree.Create expects to call os.MkdirAll, so
	// creation fails reliably without chmod tricks.
	tmpDir := t.TempDir()
	sessPath := filepath.Join(tmpDir, "sessions")
	if err := os.WriteFile(sessPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	obs := newRecordingObserver()
	step := &ExecuteStep{
		Spec:     newReplaySpec("# Plan\nDo work."),
		RepoPath: repoDir,
		WorktreeSpecFn: func(wtPath string) harness.ProcessSpec {
			return newReplaySpec("# Plan\nDo work.")
		},
	}

	sc := StepContext{
		Exec:      &fixturePlayer{path: "../harness/testdata/worker_stream_sample.jsonl"},
		Obs:       obs,
		Artifacts: NoopArtifactSink(),
		Sessions:  rundir.Dir{Path: sessPath},
		Log:       slog.Default(),
		RepoPath:  repoDir,
	}

	_, err := step.Run(context.Background(), ExecuteInput{
		RunID:     "degrade-gate",
		FinalPlan: "# Plan\nDo work.",
	}, sc)

	// Fail closed: isolation failure must surface as an error...
	if err == nil {
		t.Fatal("INV-P3-DEGRADE: worktree-creation failure was swallowed — Run returned nil (silent fallback to live repo)")
	}
	if !strings.Contains(err.Error(), "worktree") {
		t.Errorf("INV-P3-DEGRADE: error should name the worktree isolation failure, got: %v", err)
	}

	// ...and as an observable worker failure event.
	if !obs.Failed("worker") {
		t.Error("INV-P3-DEGRADE: Observer.AgentFailed was not called for the worker — the TUI/pipeline never learns isolation was lost")
	}
}
