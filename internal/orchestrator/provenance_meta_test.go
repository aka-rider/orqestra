package orchestrator

// WP11 gate (c): report provenance must be present in every persisted
// *_meta.json for a report-producing step — read back through
// rundir.LoadStepMetas, the same path run_history.go/the TUI use, not just
// asserted against the in-memory StepMeta value a step happened to build.
// RED before WP11: rundir.StepMeta had no report_tier/report_source fields
// at all, so this assertion could not even be expressed.

import (
	"context"
	"log/slog"
	"os/exec"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/rundir"
)

func TestReportProvenance_PersistedInEveryStepMeta(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	repoRoot := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed (repo setup): %v\n%s", args, err, out)
		}
	}

	dir, err := rundir.Create(repoRoot, "provenance-run")
	if err != nil {
		t.Fatalf("rundir.Create: %v", err)
	}
	artifacts := NewArtifactSink(dir, slog.Default())

	// --- Architect (DeliberateStep, no critic) — SubmitReport tier 1. ---
	archStore := &fakeReportStore{reports: map[string]string{
		"architect": "# Plan\n\n## Goal\nBuild the thing.\n\n## Work Packages\n### 1. Do it\n",
	}}
	archExec := &sequencedExecutor{
		results: []harness.RunResult{{SessionID: "arch-sid-prov"}},
		errs:    []error{nil},
	}
	archSC := StepContext{
		Exec:      archExec,
		Obs:       NewObsStore(),
		Artifacts: artifacts,
		Log:       slog.Default(),
		RepoPath:  repoRoot,
		Reports:   archStore,
	}
	deliberate := &DeliberateStep{ArchSpec: harness.ProcessSpec{AgentID: "architect"}, HasCritic: false}
	plan, err := deliberate.Run(context.Background(), DeliberateInput{OriginalPrompt: "build the thing"}, archSC)
	if err != nil {
		t.Fatalf("DeliberateStep.Run: %v", err)
	}

	// --- Worker (ExecuteStep) — raw output tier (no SubmitReport). ---
	workerExec := &sequencedExecutor{
		results: []harness.RunResult{{SessionID: "worker-sid-prov", Output: "worker finished the task"}},
		errs:    []error{nil},
	}
	workerSC := StepContext{
		Exec:      workerExec,
		Obs:       NewObsStore(),
		Artifacts: artifacts,
		Sessions:  dir,
		Log:       slog.Default(),
		RepoPath:  repoRoot,
	}
	execute := &ExecuteStep{Spec: harness.ProcessSpec{AgentID: "worker"}, RepoPath: repoRoot}
	if _, err := execute.Run(context.Background(), ExecuteInput{FinalPlan: plan.Markdown, RunID: "provenance-run"}, workerSC); err != nil {
		t.Fatalf("ExecuteStep.Run: %v", err)
	}

	// --- Read back through the SAME schema run_history.go/the TUI use. ---
	metas, err := dir.LoadStepMetas()
	if err != nil {
		t.Fatalf("LoadStepMetas: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("LoadStepMetas returned %d metas, want 2 (architect, worker)", len(metas))
	}

	var archMeta, workerMeta *rundir.StepMeta
	for i := range metas {
		switch metas[i].AgentID {
		case "architect":
			archMeta = &metas[i]
		case "worker":
			workerMeta = &metas[i]
		}
	}
	if archMeta == nil {
		t.Fatal("no persisted architect_meta.json found via LoadStepMetas")
	}
	if workerMeta == nil {
		t.Fatal("no persisted worker_meta.json found via LoadStepMetas")
	}

	if archMeta.ReportTier != 1 || archMeta.ReportSource != SourceSubmitReport {
		t.Errorf("architect meta provenance = tier=%d source=%q, want tier=1 source=%q",
			archMeta.ReportTier, archMeta.ReportSource, SourceSubmitReport)
	}
	if workerMeta.ReportTier == 0 || workerMeta.ReportSource != SourceRawOutput {
		t.Errorf("worker meta provenance = tier=%d source=%q, want a populated tier and source=%q",
			workerMeta.ReportTier, workerMeta.ReportSource, SourceRawOutput)
	}
}
