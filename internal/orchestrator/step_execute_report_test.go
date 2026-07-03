package orchestrator

// WP11 gate (e): ExecuteStep's worker report harvest is SubmitReport when
// present, else raw output — and the harvested provenance is both persisted
// (worker_meta.json) and observed (Observer.ReportHarvested), never silent.

import (
	"context"
	"log/slog"
	"os/exec"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// reportHarvestRecorder is an Observer that records ReportHarvested calls.
type reportHarvestRecorder struct {
	*recordingObserver
	got []ReportProvenance
}

func (r *reportHarvestRecorder) ReportHarvested(_ AgentID, prov ReportProvenance) {
	r.got = append(r.got, prov)
}

func setupExecuteStepGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
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
	return repoDir
}

func TestExecuteStep_ReportHarvest_SubmitReportPreferredOverRawOutput(t *testing.T) {
	repoDir := setupExecuteStepGitRepo(t)

	store := &fakeReportStore{reports: map[string]string{"worker": "the worker's SubmitReport text"}}
	execStub := &sequencedExecutor{
		results: []harness.RunResult{{SessionID: "worker-sid", Output: "raw stdout, should be ignored"}},
		errs:    []error{nil},
	}
	recorder := &reportHarvestRecorder{recordingObserver: newRecordingObserver()}
	sc := StepContext{
		Exec:      execStub,
		Obs:       recorder,
		Artifacts: NoopArtifactSink(),
		Log:       slog.Default(),
		RepoPath:  repoDir,
		Reports:   store,
	}
	step := &ExecuteStep{Spec: harness.ProcessSpec{AgentID: "worker"}, RepoPath: repoDir}

	out, err := step.Run(context.Background(), ExecuteInput{FinalPlan: "# Plan\nDo work.", RunID: "report-gate"}, sc)
	if err != nil {
		t.Fatalf("ExecuteStep.Run: %v", err)
	}
	if out.WorkOutput != "the worker's SubmitReport text" {
		t.Errorf("WorkOutput = %q, want the SubmitReport text", out.WorkOutput)
	}
	if len(recorder.got) != 1 || recorder.got[0].Source != SourceSubmitReport {
		t.Errorf("ReportHarvested provenance = %+v, want one entry with Source=%q", recorder.got, SourceSubmitReport)
	}
}

func TestExecuteStep_ReportHarvest_FallsBackToRawOutput(t *testing.T) {
	repoDir := setupExecuteStepGitRepo(t)

	execStub := &sequencedExecutor{
		results: []harness.RunResult{{SessionID: "worker-sid-2", Output: "raw worker output, no SubmitReport"}},
		errs:    []error{nil},
	}
	recorder := &reportHarvestRecorder{recordingObserver: newRecordingObserver()}
	sc := StepContext{
		Exec:      execStub,
		Obs:       recorder,
		Artifacts: NoopArtifactSink(),
		Log:       slog.Default(),
		RepoPath:  repoDir,
		// No Reports store — SubmitReport is simply unavailable.
	}
	step := &ExecuteStep{Spec: harness.ProcessSpec{AgentID: "worker"}, RepoPath: repoDir}

	out, err := step.Run(context.Background(), ExecuteInput{FinalPlan: "# Plan\nDo work.", RunID: "report-gate-2"}, sc)
	if err != nil {
		t.Fatalf("ExecuteStep.Run: %v", err)
	}
	if out.WorkOutput != "raw worker output, no SubmitReport" {
		t.Errorf("WorkOutput = %q, want the raw output", out.WorkOutput)
	}
	if len(recorder.got) != 1 || recorder.got[0].Source != SourceRawOutput {
		t.Errorf("ReportHarvested provenance = %+v, want one entry with Source=%q", recorder.got, SourceRawOutput)
	}
}
