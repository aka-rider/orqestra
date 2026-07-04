//go:build darwin && integration

package harness_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// countingSink is a concurrency-safe harness.Sink for tests. Observe is called
// from harness.Run's dedicated goroutine, so access is mutex-guarded.
type countingSink struct {
	mu        sync.Mutex
	count     int
	sessionID string
}

func (s *countingSink) Observe(ev harness.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	if ev.SessionID != "" && s.sessionID == "" {
		s.sessionID = ev.SessionID
	}
}

func (s *countingSink) snapshot() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, s.sessionID
}

// driveReplayRun builds the replay stub and runs the REAL harness.Run against it
// through the seatbelt sandbox, injecting the stub via the `binary` knob (no
// test-only seam). It reports whether Run returned within grace and what the sink
// observed. This is the deterministic replay seam on the post-refactor harness.
func driveReplayRun(t *testing.T, grace time.Duration) (returned bool, events int, sessionID string) {
	t.Helper()

	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	repoRoot := repoRootDir(t)
	workspace := t.TempDir()

	// Build the replay stub into its own dir so we can exec-allow-list exactly it.
	binDir := t.TempDir()
	stub := filepath.Join(binDir, "replayclaude")
	build := exec.Command("go", "build", "-o", stub, "./cmd/replayclaude")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build replay stub: %v\n%s", err, out)
	}

	// Place the recording the stub replays in the workspace (== process CWD).
	rec, err := os.ReadFile(filepath.Join(repoRoot, "internal", "harness", "testdata", "worker_stream_sample.jsonl"))
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".orqestra-replay.ndjson"), rec, 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}

	spec := harness.ProcessSpec{
		Model:   harness.ModelSpec{Provider: "native"}, // no model-env override
		Binary:  stub,
		Prompt:  "go",
		WorkDir: workspace,
		Sandbox: harness.SandboxConfig{
			RepoPath: workspace,
			Writable: true,
			Execs:    []string{binDir},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := &countingSink{}
	errCh := make(chan error, 1)
	go func() {
		_, e := harness.Run(ctx, spec, nil, sink)
		errCh <- e
	}()

	select {
	case <-errCh:
		returned = true
	case <-time.After(grace):
		returned = false
		cancel() // unblock Run so its goroutine can exit
	}
	n, sid := sink.snapshot()
	return returned, n, sid
}

// repoRootDir walks up from the test's working directory to the module root.
func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above test working directory")
		}
		dir = parent
	}
}

// TestHarnessRun_TerminatesWhenProcessExits is the gate for INV-H1-CLOSE and
// INV-H2-SESSIONID.
//
// INV-H1-CLOSE: harness.Run must return exactly once when the replayed process
// exits — never leave a consumer hanging (the DEFECT-01 failure mode, fixed by
// the harness refactor).
//
// INV-H2-SESSIONID: the session id must be captured from the stream via
// EventSessionStart (the DEFECT-05 failure mode, fixed when parseStreamLines
// began emitting EventSessionStart).
func TestHarnessRun_TerminatesWhenProcessExits(t *testing.T) {
	returned, events, sessionID := driveReplayRun(t, 15*time.Second)
	if !returned {
		t.Fatal("INV-H1-CLOSE: harness.Run did not return after the process exited (hang)")
	}
	if events == 0 {
		t.Fatal("inconclusive: replay stub produced no events (sandbox/exec setup issue)")
	}
	if sessionID == "" {
		t.Fatal("INV-H2-SESSIONID: no SessionID observed from the stream")
	}
}
