package harness_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// discardSink is a no-op harness.Sink for tests that don't care about events.
type discardSink struct{}

func (discardSink) Observe(harness.Event) {}

// fixturePath resolves a script under testdata, skipping the test if it isn't
// present/executable (defensive — these fixtures ship with the package).
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("resolve fixture path %s: %v", name, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture %s: %v", path, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("fixture %s is not executable", path)
	}
	return path
}

// waitForFile polls for path to exist and contain content, up to timeout. It
// exists to synchronize against an external subprocess's filesystem write —
// there is no channel/context to observe that side effect directly.
func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return strings.TrimSpace(string(data))
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s to be written", path)
		}
		<-ticker.C
	}
}

// TestRunCancelKillsProcessGroup is the QA gate for WP1 (J32/J15): on ctx
// cancel, harness.Run must SIGKILL the whole process group, not just the
// direct child, so an orphaned grandchild holding stdout open can never wedge
// Run forever.
//
// The fixture (testdata/hold_stdout.sh) plays the process-group leader: it
// backgrounds a grandchild that ignores SIGTERM and inherits stdout, then
// exits immediately — reproducing the exact defect scenario (sandbox-exec
// dies, the claude/node grandchild survives holding the pipe open).
func TestRunCancelKillsProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	script := fixturePath(t, "hold_stdout.sh")

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "leader.pid")

	// buildEnvFromSpec forwards the test process's os.Environ() into the
	// child (minus a small blocklist), so this env var reaches the fixture.
	t.Setenv("ORQESTRA_TEST_PIDFILE", pidFile)

	spec := harness.ProcessSpec{
		Model:  harness.ModelSpec{Provider: "native"},
		Binary: script,
		Prompt: "unused",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		err error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		_, err := harness.Run(ctx, spec, nil, discardSink{})
		resultCh <- outcome{err: err}
	}()

	// Synchronize on the fixture actually starting (and thus the grandchild
	// actually being alive) before cancelling — this proves the group was
	// really alive at cancel time, not already gone for unrelated reasons.
	pidStr := waitForFile(t, pidFile, 5*time.Second)
	pgid, err := strconv.Atoi(pidStr)
	if err != nil {
		t.Fatalf("parse pid file contents %q: %v", pidStr, err)
	}

	cancel()

	select {
	case out := <-resultCh:
		if !errors.Is(out.err, context.Canceled) {
			t.Fatalf("Run returned err=%v, want context.Canceled", out.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s after cancel — process group not killed (J32/J15 regression: hang)")
	}

	// The whole process group (leader + grandchild) must be dead: kill(2) with
	// signal 0 only checks existence/permission, sending nothing.
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group %d still alive after cancel: kill(-pgid,0) = %v, want ESRCH", pgid, err)
	}
}

// TestRunJoinsStdinWriterGoroutine is a best-effort regression check for J42:
// harness.Run must join the stdin-writer goroutine before returning, not just
// signal it via runDone. It uses a black-box goroutine-count check with
// retries (as opposed to a hard immediate assertion) because the exact
// scheduling race the join fixes is not independently observable from outside
// the package — see the WP1 report for why this is not claimed as a strict
// RED-first-provable gate the way TestRunCancelKillsProcessGroup is.
func TestRunJoinsStdinWriterGoroutine(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	script := fixturePath(t, "quick_exit.sh")

	spec := harness.ProcessSpec{
		Model:  harness.ModelSpec{Provider: "native"},
		Binary: script,
		Prompt: "unused",
	}

	// Non-nil, never-closed, never-sent-to input plane: the stdin writer
	// goroutine blocks in its select until runDone/ctx.Done fires internally.
	in := make(chan harness.Message)

	ctx := context.Background()

	baseline := runtime.NumGoroutine()

	if _, err := harness.Run(ctx, spec, in, discardSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Poll for goroutine count to settle back to baseline. A leaked
	// stdin-writer goroutine would never join runDone-having-already-fired
	// (it already has), so if it's still counted here after retries, Run
	// returned without waiting for it — a genuine leak, not scheduling noise.
	deadline := time.Now().Add(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		last := runtime.NumGoroutine()
		if last <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count did not settle after Run returned: baseline=%d, got=%d (stdin-writer goroutine leak, J42)", baseline, last)
		}
		<-ticker.C
	}
}
