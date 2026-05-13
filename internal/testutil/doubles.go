package testutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// FakeCall defines the response for one FakeRunner invocation.
type FakeCall struct {
	Output    string
	SessionID string
	Usage     harness.TokenUsage
	Err       error
}

// FakeRunner is a test double for harness.CLIRunner and harness.ContinuableRunner.
// All three methods draw from Calls in order; the last entry is reused once exhausted.
type FakeRunner struct {
	Calls []FakeCall
	mu    sync.Mutex
	n     int
}

func (f *FakeRunner) next() FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Calls) == 0 {
		return FakeCall{}
	}
	idx := f.n
	if idx >= len(f.Calls) {
		idx = len(f.Calls) - 1
	} else {
		f.n++
	}
	return f.Calls[idx]
}

func (f *FakeRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	out := f.next()
	return harness.RunResult{Output: out.Output, SessionID: out.SessionID, Usage: out.Usage}, out.Err
}

func (f *FakeRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	out := f.next()
	return harness.RunResult{Output: out.Output, SessionID: out.SessionID, Usage: out.Usage}, out.Err
}

func (f *FakeRunner) RunContinue(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	out := f.next()
	return harness.RunResult{Output: out.Output, SessionID: out.SessionID, Usage: out.Usage}, out.Err
}

// MustTempHome sets HOME to a fresh temp dir for the duration of the test.
// Tests calling MustTempHome must NOT call t.Parallel() — HOME is process-wide.
func MustTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

// SetupPlanFile creates a plan file and session JSONL under the current HOME.
// Call MustTempHome before SetupPlanFile.
func SetupPlanFile(t *testing.T, sessionID, planContent string) {
	t.Helper()
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		t.Fatal("HOME not set; call MustTempHome before SetupPlanFile")
	}

	plansDir := filepath.Join(home, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(plansDir, sessionID+"-plan.md")
	if err := os.WriteFile(planFile, []byte(planContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(home, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	jsonlContent := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, planFile)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ValidPlanMarkdown returns a minimal valid plan document for use in tests.
func ValidPlanMarkdown() string {
	return "# Plan\n\n## Goal\nAdd feature X.\n\n## Work Packages\n\n### 1. Add X\n\n**Steps:**\n1. Create pkg/x.go\n\n**Done when:**\n- go test ./pkg passes"
}
