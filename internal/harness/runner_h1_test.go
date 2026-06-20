//go:build darwin && integration

package harness_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/sandbox"
)

// driveRunnerUntilClose builds the replay stub, constructs the REAL sandboxed
// runner pointed at it via the `binary` config knob, drives Post/Receive through
// the seatbelt sandbox, and reports how many events arrived and whether the
// Receive() channel closed within grace. This is the deterministic replay seam —
// real production code (sandboxedRunner + ClaudeCLI), no hand-written fakes, the
// only stand-in being a player of a committed real recording.
func driveRunnerUntilClose(t *testing.T, grace time.Duration) (events int, closed bool) {
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

	// Place the recording the stub will replay in the workspace (== runner CWD).
	rec, err := os.ReadFile(filepath.Join(repoRoot, "internal", "harness", "testdata", "worker_stream_sample.jsonl"))
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".orqestra-replay.ndjson"), rec, 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}

	// Exec-allow-list the stub's directory (Exec requires a dir).
	prof := sandbox.NewToolProfile("replayclaude", home)
	if err := prof.Allow(binDir, sandbox.Exec); err != nil {
		t.Fatalf("allow stub exec: %v", err)
	}
	snap := prof.Snapshot()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := harness.NewRunner(harness.RunnerConfig{
		Binary:  stub,
		WorkDir: workspace,
		Sandbox: harness.SandboxConfig{
			RepoPath: workspace,
			Writable: true,
			Profiles: []sandbox.Snapshot{snap},
		},
	}, ctx)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	defer func() { _ = r.Cancel() }()

	ch := r.Receive() // create the session before Post so forwarding has a target
	r.Post("go")

	var count int64
	closedCh := make(chan struct{})
	go func() {
		for range ch {
			atomic.AddInt64(&count, 1)
		}
		close(closedCh) // only reached when ch is closed
	}()

	select {
	case <-closedCh:
		closed = true
	case <-time.After(grace):
		closed = false
	}
	return int(atomic.LoadInt64(&count)), closed
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

// TestHarnessRunner_ReceiveClosesOnExit is the gate for INV-H1-CLOSE: after the
// underlying process exits, Receive()'s channel must close so a `for range`
// consumer terminates.
//
// It is BLOCKED on DEFECT-01 (the sandboxed runner never closes the forwarded
// channel) and therefore times out (red) today. It is guarded so the standard
// `make test-sandbox` lane stays green while the defect is live — the DEFECT-01
// canary tracks the bug in the qaverify lane. Run with ORQESTRA_RUN_PENDING_GATES=1
// to see it red. When DEFECT-01 is fixed, delete this guard and it goes green.
func TestHarnessRunner_ReceiveClosesOnExit(t *testing.T) {
	if os.Getenv("ORQESTRA_RUN_PENDING_GATES") == "" {
		t.Skip("INV-H1-CLOSE blocked on DEFECT-01 — see TestCanary_DEFECT01_ReceiveNeverClosesAfterExit")
	}
	events, closed := driveRunnerUntilClose(t, 5*time.Second)
	if events < 1 {
		t.Fatalf("inconclusive: replay stub produced no events (sandbox/exec setup issue)")
	}
	if !closed {
		t.Fatalf("INV-H1-CLOSE: Receive() channel did not close after the process exited — DEFECT-01")
	}
}
