package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// Ensure FakeRunner implements Runner.
var _ harness.Runner = (*FakeRunner)(nil)

// FakeCall defines the response for one FakeRunner invocation.
type FakeCall struct {
	Output    string
	SessionID string
	Usage     harness.TokenUsage
	Err       error
	OnCall    func(callIndex int) // called with the 0-based call index before returning
}

// FakeRunner is a test double for harness.Runner.
// Each Post() call consumes the next FakeCall, builds a closed buffered
// channel containing the simulated events, and stores it for Receive().
// Callers must call Post() before Receive() on each turn — matching the
// contract enforced by the real Runner after the SetEvents removal.
type FakeRunner struct {
	Calls     []FakeCall
	mu        sync.Mutex
	n         int
	events    <-chan harness.Event
	storedSID string
}

func (f *FakeRunner) next() FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.Calls) == 0 || f.n >= len(f.Calls) {
		return FakeCall{}
	}
	idx := f.n
	f.n++
	call := f.Calls[idx]
	if call.OnCall != nil {
		call.OnCall(idx)
	}
	return call
}

func (f *FakeRunner) Post(msg string) {
	out := f.next()

	var evs []harness.Event
	if out.Err != nil {
		evs = []harness.Event{
			{Kind: harness.EventError, Text: out.Err.Error(), IsError: true},
		}
	} else {
		evs = []harness.Event{
			{Kind: harness.EventSessionStart, SessionID: out.SessionID},
			{Kind: harness.EventChunk, Text: out.Output},
			{Kind: harness.EventUsage, Input: out.Usage.Input, Output: out.Usage.Output},
			{Kind: harness.EventSessionDone},
		}
	}

	ch := make(chan harness.Event, len(evs))
	for _, ev := range evs {
		ch <- ev
	}
	close(ch)

	f.mu.Lock()
	f.events = ch
	f.storedSID = out.SessionID
	f.mu.Unlock()
}

func (f *FakeRunner) Receive() <-chan harness.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		ch := make(chan harness.Event)
		close(ch)
		return ch
	}
	return f.events
}

func (f *FakeRunner) ExtractPlan(ctx context.Context) (string, error) {
	sid := f.SessionID()
	if sid == "" {
		return "", fmt.Errorf("no session ID")
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("HOME not set")
	}
	planFile := filepath.Join(home, ".claude", "plans", sid+"-plan.md")
	data, err := os.ReadFile(planFile)
	if err != nil {
		return "", fmt.Errorf("read plan file %s: %w", planFile, err)
	}
	return string(data), nil
}

func (f *FakeRunner) SessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.storedSID
}

func (f *FakeRunner) Cancel() error {
	return nil
}

var _ harness.Runner = (*FakeRunner)(nil)

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
