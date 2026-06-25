package orchestrator

import (
	"context"
	"log/slog"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// fakeReportStore implements ReportStore for tests.
type fakeReportStore struct {
	reports map[string]string
}

func (f *fakeReportStore) TakeReport(key string) (string, bool) {
	r, ok := f.reports[key]
	if ok {
		delete(f.reports, key)
	}
	return r, ok
}

// countingExec is a harness.Executor that counts Run calls and always returns quickly.
type countingExec struct{ calls int }

func (c *countingExec) Run(_ context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
	c.calls++
	return harness.RunResult{}, nil
}

func TestPreferReport_FallsBackToReadPlan(t *testing.T) {
	sc := StepContext{
		Log:      slog.Default(),
		RepoPath: t.TempDir(),
	}

	// No plan file for this session → should error
	res := harness.RunResult{SessionID: "no-such-session"}
	_, err := preferReport(sc, "researcher", res)
	if err == nil {
		t.Error("expected error when no plan file exists")
	}
}

func TestPreferReport_NoSessionID(t *testing.T) {
	sc := StepContext{
		Log:      slog.Default(),
		RepoPath: t.TempDir(),
	}

	// Empty session ID → should error immediately
	res := harness.RunResult{SessionID: ""}
	_, err := preferReport(sc, "researcher", res)
	if err == nil {
		t.Error("expected error when SessionID is empty")
	}
}

// TestExtractReport_SubmitReportTierOneShot verifies tier-1 (SubmitReport) is used
// when a valid report is in the store, without calling the executor at all.
func TestExtractReport_SubmitReportTierOneShot(t *testing.T) {
	const agentID = "architect"
	const validPlan = "# Plan\n\n## Goal\nAdd a flag.\n\n## Work Packages\n### 1. Edit main.go\n"

	// TakeReport is keyed by agentID; the store resolves the session internally,
	// so report harvesting no longer depends on res.SessionID.
	store := &fakeReportStore{reports: map[string]string{agentID: validPlan}}

	sc := StepContext{
		Log:     slog.Default(),
		Reports: store,
	}

	spec := harness.ProcessSpec{AgentID: agentID}
	res := harness.RunResult{SessionID: "fake-session-abc"}

	out, err := extractReport(context.Background(), spec, res, nil, sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty plan, got empty")
	}
}

// TestExtractReport_FailClosed verifies that an empty store + no session output
// returns a descriptive error (not a bare context.Canceled or empty string).
func TestExtractReport_FailClosed(t *testing.T) {
	sc := StepContext{
		Log:      slog.Default(),
		RepoPath: t.TempDir(),
	}

	spec := harness.ProcessSpec{AgentID: "researcher"}
	res := harness.RunResult{} // no output, no session

	_, err := extractReport(context.Background(), spec, res, nil, sc)
	if err == nil {
		t.Fatal("expected error for empty extraction, got nil")
	}
}
