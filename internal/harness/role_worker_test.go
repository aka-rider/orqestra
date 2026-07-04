//go:build darwin

package harness_test

// INV-ROLE-WORKER: the worker agent's real capability gate.
//
// The worker runs under the real leash-backed seatbelt sandbox with the repo
// writable. Its real, security-load-bearing capability is: it can write
// files inside its workspace and run shell commands there, but writes
// OUTSIDE the workspace are denied. This exercises the production sandbox
// path end to end (harness.Run -> startSandboxed -> leash.Execute) with a
// real subprocess — no fakes. A broken worker grant flips one of these
// assertions and turns this RED in seconds, without running the pipeline.
//
// Ported from the deleted internal/sandbox/role_worker_test.go, which called
// sandbox.New/sb.Wrap directly — bypassing harness.Run entirely. This
// version drives the SAME real production call path every worker invocation
// uses, closing that gap.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

func TestRole_Worker_WritesInWorkspaceDeniedOutside(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	repoDir := t.TempDir() // the worker's writable workspace

	// The "outside" target must be in a region seatbelt denies. The system temp
	// dir is broadly writable (leash's own base rules), so a sibling under HOME
	// is the honest out-of-bounds location (same choice the deleted test made).
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	outsideDir := filepath.Join(home, ".seatbelt-test-role-worker-breach")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outsideDir)

	insideFile := filepath.Join(repoDir, "worker-wrote.go")
	breachFile := filepath.Join(outsideDir, "breach.txt")

	spec := harness.ProcessSpec{
		Model:   harness.ModelSpec{Provider: "native"},
		Binary:  fixturePath(t, "workspace_write_probe.sh"),
		Prompt:  "unused",
		WorkDir: repoDir,
		Sandbox: harness.SandboxConfig{
			RepoPath: repoDir,
			Writable: true, // worker role
			// Seatbelt's process-exec check applies to the fixture script
			// file itself, not just its content — leash's own implicit cwd
			// grant is Read-only (defaultCwdPermission), so an explicit
			// Execs grant on testdata/ is required to exec a fixturePath
			// binary under a sandboxed spec. See testdataExecDir.
			Execs: []string{testdataExecDir(t)},
			Env: []string{
				"ORQESTRA_TEST_INSIDE=" + insideFile,
				"ORQESTRA_TEST_OUTSIDE=" + breachFile,
			},
		},
	}

	if _, err := harness.Run(context.Background(), spec, nil, discardSink{}); err != nil {
		t.Fatalf("harness.Run: %v", err)
	}

	// Capability: write a code file inside the workspace via a real subprocess.
	if data, err := os.ReadFile(insideFile); err != nil {
		t.Fatalf("worker write inside workspace did not land: %v", err)
	} else if !bytes.Contains(data, []byte("package main")) {
		t.Fatalf("worker write content wrong: %q", data)
	}

	// Containment: a write OUTSIDE the workspace must be denied (no file created).
	if _, err := os.Stat(breachFile); err == nil {
		os.Remove(breachFile)
		t.Fatal("INV-ROLE-WORKER: SECURITY FAILURE — worker wrote outside its workspace")
	}
}
