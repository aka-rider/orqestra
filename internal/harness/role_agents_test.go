//go:build darwin

package harness_test

// INV-ROLE-RESEARCH / INV-ROLE-CRITIC: the read-only agents' real capability gate.
//
// The researcher and critic run as a real `claude` subprocess under the seatbelt
// sandbox in read-only mode, and the harness must spawn them, stream-parse their
// output, and capture the session id. This drives the REAL harness.Run against
// the replay stub via the `binary` knob (no fake executor, no fixturePlayer) — the
// subprocess seam that no other test in `make test` exercises. A break in spec
// assembly, subprocess spawn, sandbox wrap, or stream parsing turns this RED in
// seconds, without running the pipeline.

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

// roleSink is a concurrency-safe harness.Sink (Observe runs on harness.Run's
// goroutine). Named distinctly from runner_h1_test.go's countingSink so both
// files coexist under the `darwin && integration` build.
type roleSink struct {
	mu        sync.Mutex
	count     int
	sessionID string
}

func (s *roleSink) Observe(ev harness.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	if ev.SessionID != "" && s.sessionID == "" {
		s.sessionID = ev.SessionID
	}
}

func (s *roleSink) snapshot() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, s.sessionID
}

func roleRepoRoot(t *testing.T) string {
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

// driveReadOnlyRoleRun runs harness.Run against the replay stub under a read-only
// seatbelt sandbox and returns what the sink observed. role only labels the run.
func driveReadOnlyRoleRun(t *testing.T, role string) (events int, sessionID string) {
	t.Helper()

	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	repoRoot := roleRepoRoot(t)
	workspace := t.TempDir()

	binDir := t.TempDir()
	stub := filepath.Join(binDir, "replayclaude")
	build := exec.Command("go", "build", "-o", stub, "./cmd/replayclaude")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build replay stub: %v\n%s", err, out)
	}

	rec, err := os.ReadFile(filepath.Join(repoRoot, "internal", "harness", "testdata", "worker_stream_sample.jsonl"))
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".orqestra-replay.ndjson"), rec, 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}

	spec := harness.ProcessSpec{
		Model:   harness.ModelSpec{Provider: "native"},
		Binary:  stub,
		Prompt:  "go " + role,
		WorkDir: workspace,
		Sandbox: harness.SandboxConfig{
			RepoPath: workspace,
			Writable: false, // read-only role (researcher / critic)
			Execs:    []string{binDir},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := &roleSink{}
	errCh := make(chan error, 1)
	go func() {
		_, e := harness.Run(ctx, spec, nil, sink)
		errCh <- e
	}()

	select {
	case <-errCh:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("harness.Run did not return within grace (subprocess hang)")
	}
	return sink.snapshot()
}

func TestRole_Researcher_RealRun(t *testing.T) {
	// INV-ROLE-RESEARCH
	events, sid := driveReadOnlyRoleRun(t, "researcher")
	if events == 0 {
		t.Fatal("INV-ROLE-RESEARCH: researcher subprocess produced no parsed events (spec/spawn/sandbox/parse broken)")
	}
	if sid == "" {
		t.Fatal("INV-ROLE-RESEARCH: no session id captured from the researcher stream")
	}
}

func TestRole_Critic_RealRun(t *testing.T) {
	// INV-ROLE-CRITIC
	events, sid := driveReadOnlyRoleRun(t, "critic")
	if events == 0 {
		t.Fatal("INV-ROLE-CRITIC: critic subprocess produced no parsed events (spec/spawn/sandbox/parse broken)")
	}
	if sid == "" {
		t.Fatal("INV-ROLE-CRITIC: no session id captured from the critic stream")
	}
}
