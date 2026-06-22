//go:build darwin

package sandbox_test

// INV-ROLE-WORKER: the worker agent's real capability gate.
//
// The worker runs under the real seatbelt sandbox with the repo writable. Its
// real, security-load-bearing capability is: it can write files inside its
// workspace and run shell commands there, but writes OUTSIDE the workspace are
// denied. This exercises the production sandbox (sandbox.New + sb.Run) with a
// real subprocess — no fakes. A broken worker profile flips one of these
// assertions and turns this RED in seconds, without running the pipeline.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/sandbox"
)

func TestRole_Worker_WritesInWorkspaceDeniedOutside(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	repoDir := t.TempDir()    // the worker's writable workspace
	sessionDir := t.TempDir() // session artifacts

	// The "outside" target must be in a region seatbelt denies. The system temp
	// dir is broadly writable, so a sibling under HOME is the honest out-of-bounds
	// location (same choice as TestDeny_WriteOutsideWorkspace).
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	outsideDir := filepath.Join(home, ".seatbelt-test-role-worker-breach")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outsideDir)

	sb, err := sandbox.New(sandbox.Config{
		RepoPath:     repoDir,
		SessionPath:  sessionDir,
		RepoWritable: true, // worker role
	})
	if err != nil {
		t.Fatalf("sandbox.New (worker): %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	run := func(script string) error {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
		cmd.Stdout = &bytes.Buffer{}
		cmd.Stderr = &bytes.Buffer{}
		return sb.Run(ctx, cmd)
	}

	// Capability: write a code file inside the workspace via a shell command.
	insideFile := filepath.Join(repoDir, "worker-wrote.go")
	if err := run("echo 'package main' > '" + insideFile + "'"); err != nil {
		t.Fatalf("worker shell command failed inside workspace: %v", err)
	}
	if data, err := os.ReadFile(insideFile); err != nil {
		t.Fatalf("worker write inside workspace did not land: %v", err)
	} else if !bytes.Contains(data, []byte("package main")) {
		t.Fatalf("worker write content wrong: %q", data)
	}

	// Containment: a write OUTSIDE the workspace must be denied (no file created).
	breachFile := filepath.Join(outsideDir, "breach.txt")
	_ = run("echo BREACH > '" + breachFile + "'") // expected to be denied by seatbelt
	if _, err := os.Stat(breachFile); err == nil {
		os.Remove(breachFile)
		t.Fatal("INV-ROLE-WORKER: SECURITY FAILURE — worker wrote outside its workspace")
	}
}
