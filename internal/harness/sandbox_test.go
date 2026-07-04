//go:build darwin

package harness_test

// Consolidated leash-backed sandbox boundary tests, driven entirely through
// harness.Run (the real production call path), not sandbox internals — this
// package no longer has SBPL/path/env-merge internals of its own to test;
// leash owns that (leash/sandbox/{path,env,builder,sandbox}_test.go) and has
// its own coverage. No ANTHROPIC_API_KEY or real claude CLI required: every
// test here drives a small fixture script, so this file needs only
// //go:build darwin (no integration tag) — see internal/harness/CLAUDE.md's
// test matrix.
//
// Ported/consolidated from the deleted internal/sandbox/sandbox_test.go:
// production never constructs "read-only repo with no worktree" or
// "worktree with no repo grant" for the worker role in isolation — the real
// shape is always both axes together (Writable:false + WorktreePath set) —
// so TestSandboxed_ReadOnlyRepoWritableWorktree asserts both halves of that
// one real shape in a single sandbox instance rather than as two synthetic
// ones. TestSandboxed_ReadGrantBoundary and TestSandboxed_ExecGrantBoundary
// cover the explicit Reads/Execs grant channels (the exec case is genuinely
// new coverage: today's grant is directory-level, unlike the deleted
// package's file-level-only exec grants). TestAllow_WriteInWorkspace/
// TestDeny_WriteOutsideWorkspace/TestSeatbelt_WorkerRepoWriteAllowed's
// worker-writable-repo coverage is NOT re-ported here — it is subsumed by
// role_worker_test.go's ported INV-ROLE-WORKER test, which already asserts
// exactly that shape. TestSandboxed_BadRepoPath_ReturnsWrappedError and
// TestSandboxed_CtxCancel_ReturnsPromptly are new, not ports: they exercise
// the harness<->leash error-wrapping boundary and the sandboxed wait()
// cancellation path (exec_sandbox.go's context.WithCancelCause /
// context.Cause distinction) specifically, under -race.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// testdataExecDir returns the absolute path to this package's testdata
// directory, for tests whose spec.Binary is a fixturePath script. Seatbelt's
// process-exec check applies to the SCRIPT FILE ITSELF (checked before the
// kernel ever looks at the #! interpreter line), not just its content — so
// leash's own implicit cwd grant (defaultCwdPermission, Read-only whenever
// spec.WorkDir is set, which every test here does) is NOT sufficient to exec
// a fixturePath binary under a sandboxed spec. Every test in this file (and
// role_worker_test.go) that runs a fixture script under Sandbox must
// explicitly grant Execs on this directory.
func testdataExecDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}
	return dir
}

// TestSandboxed_ReadOnlyRepoWritableWorktree covers the CLAUDE.md "read-only
// repo + writable worktree" invariant: with Writable:false and WorktreePath
// set, a write inside the worktree succeeds and a write to the repo root is
// denied, in ONE sandbox instance — the shape production actually
// constructs (worker execution always goes through WorktreeSpecFn).
//
// Both directories are placed under HOME, NOT t.TempDir(): leash's base SBPL
// profile unconditionally grants file-read*/file-write* on the whole
// TMPDIR/private/var/folders subtree (leash/sandbox/builder.go's "Tmp
// (read+write)" rule) regardless of any explicit grant, so a t.TempDir()
// repoDir would "pass" the denial assertion for the wrong reason (always-
// writable tmp, not Writable:false actually being enforced) and a
// t.TempDir() worktreeDir wouldn't rigorously prove the explicit
// WorktreePath grant is what makes the write succeed. A sibling under HOME
// is the honest, grant-specific location (the same choice
// role_worker_test.go/TestSandboxed_ReadGrantBoundary's deny side make).
func TestSandboxed_ReadOnlyRepoWritableWorktree(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}

	repoDir := filepath.Join(home, ".seatbelt-test-readonly-repo-worktree")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(repoDir)

	worktreeDir := filepath.Join(home, ".seatbelt-test-writable-worktree")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(worktreeDir)

	insideFile := filepath.Join(worktreeDir, "artifact.json") // worktree write: must succeed
	breachFile := filepath.Join(repoDir, "breach.txt")        // repo-root write: must be denied

	spec := harness.ProcessSpec{
		Model:   harness.ModelSpec{Provider: "native"},
		Binary:  fixturePath(t, "workspace_write_probe.sh"),
		Prompt:  "unused",
		WorkDir: worktreeDir,
		Sandbox: harness.SandboxConfig{
			RepoPath:     repoDir,
			Writable:     false, // read-only repo
			WorktreePath: worktreeDir,
			Execs:        []string{testdataExecDir(t)},
			Env: []string{
				"ORQESTRA_TEST_INSIDE=" + insideFile,
				"ORQESTRA_TEST_OUTSIDE=" + breachFile,
			},
		},
	}

	if _, err := harness.Run(context.Background(), spec, nil, discardSink{}); err != nil {
		t.Fatalf("harness.Run: %v", err)
	}

	if _, err := os.Stat(insideFile); err != nil {
		t.Fatalf("worktree write did not land: %v", err)
	}
	if _, err := os.Stat(breachFile); err == nil {
		os.Remove(breachFile)
		t.Fatal("SECURITY FAILURE: readonly-repo sandbox wrote to repo root while in worktree mode")
	}
}

// TestSandboxed_ReadGrantBoundary covers SandboxConfig.Reads: an explicitly
// granted directory is readable, and a file outside every grant is denied,
// in ONE sandbox instance. allowedDir is placed under HOME, not
// t.TempDir(): leash's base rules already grant file-read* unconditionally
// on the whole TMPDIR subtree, so a t.TempDir() allowedDir would "pass"
// without the explicit Reads grant actually doing anything — see
// TestSandboxed_ReadOnlyRepoWritableWorktree's doc comment for the full
// reasoning (same root cause).
func TestSandboxed_ReadGrantBoundary(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}

	repoDir := t.TempDir() // writable — the verdict file lands here (not under test itself)

	allowedDir := filepath.Join(home, ".seatbelt-test-allow-read")
	if err := os.MkdirAll(allowedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(allowedDir)
	allowedFile := filepath.Join(allowedDir, "readable.txt")
	if err := os.WriteFile(allowedFile, []byte("ALLOWED_CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	deniedDir := filepath.Join(home, ".seatbelt-test-deny-read")
	if err := os.MkdirAll(deniedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(deniedDir)
	deniedFile := filepath.Join(deniedDir, "secret.txt")
	if err := os.WriteFile(deniedFile, []byte("TOP_SECRET_DATA"), 0o644); err != nil {
		t.Fatal(err)
	}

	verdictFile := filepath.Join(repoDir, "verdict.txt")

	spec := harness.ProcessSpec{
		Model:   harness.ModelSpec{Provider: "native"},
		Binary:  fixturePath(t, "read_probe.sh"),
		Prompt:  "unused",
		WorkDir: repoDir,
		Sandbox: harness.SandboxConfig{
			RepoPath: repoDir,
			Writable: true,
			Reads:    []string{allowedDir},
			Execs:    []string{testdataExecDir(t)},
			Env: []string{
				"ORQESTRA_TEST_READ_ALLOWED=" + allowedFile,
				"ORQESTRA_TEST_READ_DENIED=" + deniedFile,
				"ORQESTRA_TEST_VERDICT=" + verdictFile,
			},
		},
	}

	if _, err := harness.Run(context.Background(), spec, nil, discardSink{}); err != nil {
		t.Fatalf("harness.Run: %v", err)
	}

	verdict, err := os.ReadFile(verdictFile)
	if err != nil {
		t.Fatalf("verdict file not written: %v", err)
	}
	got := string(verdict)
	if !strings.Contains(got, "allowed=ALLOWED_CONTENT") {
		t.Errorf("expected the explicitly Reads-granted file to be readable, verdict: %q", got)
	}
	if strings.Contains(got, "TOP_SECRET_DATA") {
		t.Fatalf("SECURITY FAILURE: sandbox leaked file content outside the Reads grant, verdict: %q", got)
	}
	if !strings.Contains(got, "denied=DENIED") {
		t.Errorf("expected the file outside the Reads grant to be denied, verdict: %q", got)
	}
}

// TestSandboxed_ExecGrantBoundary covers SandboxConfig.Execs specifically as
// a DIRECTORY-level grant (the ./bin-style grant this migration introduces —
// the deleted package's exec grants were file-level only, so this is
// genuinely new coverage, not a straight port). A copy of a small shell
// script (quick_exit.sh — NOT a copied Apple system binary: macOS's
// platform-binary code-signing protection SIGKILLs a copy of e.g. /bin/echo
// run from anywhere outside its original path, entirely independent of
// Seatbelt, which would falsely read as an exec denial) placed inside the
// granted directory must run; the same script copied to an ungranted
// directory must not.
//
// Both directories are placed under HOME, NOT t.TempDir(): leash's own
// detect.Go tool detector unconditionally grants process-exec on the whole
// os.TempDir() subtree (to support `go run`'s compile-then-exec-from-tmpdir
// behavior — leash/detect/golang.go), so a t.TempDir() execDir/outsideDir
// would exec successfully regardless of the explicit Execs grant under
// test, on any machine with the Go toolchain installed (every dev/CI
// machine building this project). A sibling under HOME sidesteps every
// detector's grants and every base rule, so only the explicit Execs entry
// this test controls can make the difference.
func TestSandboxed_ExecGrantBoundary(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}

	repoDir := t.TempDir()

	execDir := filepath.Join(home, ".seatbelt-test-exec-granted") // granted via Execs
	if err := os.MkdirAll(execDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(execDir)

	outsideDir := filepath.Join(home, ".seatbelt-test-exec-denied") // NOT granted
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outsideDir)

	src, err := os.ReadFile(fixturePath(t, "quick_exit.sh"))
	if err != nil {
		t.Fatalf("read quick_exit.sh fixture: %v", err)
	}
	for _, dir := range []string{execDir, outsideDir} {
		if err := os.WriteFile(filepath.Join(dir, "quick_exit.sh"), src, 0o755); err != nil {
			t.Fatalf("write quick_exit.sh copy into %s: %v", dir, err)
		}
	}

	runProbe := func(bin string) error {
		spec := harness.ProcessSpec{
			Model:   harness.ModelSpec{Provider: "native"},
			Binary:  bin,
			Prompt:  "unused",
			WorkDir: repoDir,
			Sandbox: harness.SandboxConfig{
				RepoPath: repoDir,
				Writable: true,
				Execs:    []string{execDir},
			},
		}
		_, runErr := harness.Run(context.Background(), spec, nil, discardSink{})
		return runErr
	}

	if err := runProbe(filepath.Join(execDir, "quick_exit.sh")); err != nil {
		t.Fatalf("exec from the granted directory should succeed, got: %v", err)
	}
	if err := runProbe(filepath.Join(outsideDir, "quick_exit.sh")); err == nil {
		t.Fatal("SECURITY FAILURE: exec succeeded from a directory NOT in the Execs grant")
	}
}

// TestSandboxed_BadRepoPath_ReturnsWrappedError is new coverage (not a port):
// a nonexistent spec.Sandbox.RepoPath must make harness.Run return a
// non-nil, non-panicking, correctly-wrapped error — never a NonZeroExitError
// (the process never starts; leash's own grant resolution fails first) and
// never a silent success. This tests the harness<->leash error-wrapping
// boundary specifically (internal/sandbox never had this failure mode
// expressed this way, since sandbox.New's caller in exec.go wrapped its own
// distinct error prefix).
func TestSandboxed_BadRepoPath_ReturnsWrappedError(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir := t.TempDir()
	spec := harness.ProcessSpec{
		Model:   harness.ModelSpec{Provider: "native"},
		Binary:  fixturePath(t, "quick_exit.sh"), // never actually reached — grant resolution fails first
		Prompt:  "unused",
		WorkDir: workDir,
		Sandbox: harness.SandboxConfig{
			RepoPath: filepath.Join(workDir, "does-not-exist-xyz"),
			Writable: true,
		},
	}

	_, err := harness.Run(context.Background(), spec, nil, discardSink{})
	if err == nil {
		t.Fatal("expected an error for a nonexistent sandbox RepoPath, got nil")
	}
	var nzErr *harness.NonZeroExitError
	if errors.As(err, &nzErr) {
		t.Fatalf("expected a setup-failure wrap (the process never started), not a NonZeroExitError: %v", err)
	}
}

// TestSandboxed_CtxCancel_ReturnsPromptly is new coverage (not a port): ctx
// canceled while a real sandboxed subprocess is in flight must make
// harness.Run return ctx.Err() promptly. This exercises startSandboxed's own
// wait()/context.WithCancelCause/context.Cause wiring specifically — NOT
// leash's internal SIGKILL behavior (leash/sandbox's own tests cover that) —
// confirming the whole Run call unblocks once leash resolves the
// cancellation, and that wait() takes the "ctx propagated, do not read
// execErr" branch rather than the "goroutine finished" branch. Run under
// -race (make test/test-sandbox both pass -race): a version of wait() that
// read execErr unconditionally on execCtx.Done() (instead of checking
// context.Cause first) would race here, because ctx's cancellation
// propagates to execCtx.Done() immediately, well before the goroutine
// running leash.Execute has actually finished writing execErr.
func TestSandboxed_CtxCancel_ReturnsPromptly(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	repoDir := t.TempDir()
	pidFile := filepath.Join(repoDir, "leader.pid")

	spec := harness.ProcessSpec{
		Model:   harness.ModelSpec{Provider: "native"},
		Binary:  fixturePath(t, "hold_stdout.sh"),
		Prompt:  "unused",
		WorkDir: repoDir,
		Sandbox: harness.SandboxConfig{
			RepoPath: repoDir,
			Writable: true,
			Execs:    []string{testdataExecDir(t)},
			Env:      []string{"ORQESTRA_TEST_PIDFILE=" + pidFile},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		_, runErr := harness.Run(ctx, spec, nil, discardSink{})
		resultCh <- runErr
	}()

	// Synchronize on the fixture actually starting before cancelling — proves
	// leash.Execute was genuinely mid-flight at cancel time, not already done
	// for unrelated reasons.
	waitForFile(t, pidFile, 15*time.Second)
	cancel()

	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("harness.Run returned err=%v, want context.Canceled", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("harness.Run did not return within 20s after cancel — sandboxed wait() did not react to ctx cancellation")
	}
}
