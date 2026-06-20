package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

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
