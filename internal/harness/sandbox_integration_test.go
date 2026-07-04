//go:build darwin && integration

package harness_test

// Ported from the deleted internal/sandbox/sandbox_integration_test.go: these
// tests used to call sandbox.New/sb.Wrap directly (with their own raw
// exec.Cmd + execSandbox helper), bypassing harness.Run entirely — never
// actually exercising orqestra's real production call path. Ported to drive
// harness.Run instead, closing that gap. The manual detect.DetectClaude call
// is dropped: leash detects claude automatically, unconditionally, on every
// Execute call.
//
// Run with: go test ./internal/harness/ -tags 'darwin integration' -run TestClaudeCLI_InSandbox -v
//
// Requires:
// - claude CLI installed and authenticated
// - ANTHROPIC_API_KEY (skipped otherwise — OAuth-only auth is harder to
//   detect programmatically, and CI/local automation typically uses a key)

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// claudeBinaryForTest returns the claude CLI binary path — honors
// CLAUDE_BINARY for machines with a non-PATH install, defaulting to "claude"
// (the same default harness.Run itself applies for an empty spec.Binary).
func claudeBinaryForTest() string {
	if bin := os.Getenv("CLAUDE_BINARY"); bin != "" {
		return bin
	}
	return "claude"
}

// TestClaudeCLI_InSandbox verifies that claude CLI can run inside the
// leash-backed seatbelt sandbox through harness.Run end to end.
func TestClaudeCLI_InSandbox(t *testing.T) {
	// INV-P2-WRITE: claude CLI runs and writes files inside the sandbox (L3 live test)
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set — sandbox Claude CLI tests require API key authentication")
	}

	workspace := t.TempDir()

	var sandboxEnv []string
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		sandboxEnv = append(sandboxEnv, "ANTHROPIC_API_KEY="+key)
	}

	prompt := `Create a file called hello.txt in the current directory containing exactly "Hello from sandbox". Use the file write tool. Do not ask for confirmation.`
	spec := harness.ProcessSpec{
		Model:     harness.ModelSpec{Provider: "native"},
		Binary:    claudeBinaryForTest(),
		Prompt:    prompt,
		WorkDir:   workspace,
		ExtraArgs: []string{"--allowedTools", "Write"},
		Sandbox: harness.SandboxConfig{
			RepoPath: workspace,
			Writable: true,
			Env:      sandboxEnv,
		},
	}

	t.Logf("Workspace: %s", workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := harness.Run(ctx, spec, nil, nil)
	t.Logf("Output: %s", res.Output)
	if err != nil {
		t.Logf("harness.Run error: %v (stderr: %s)", err, res.Stderr)
		t.Fatalf("Exec failed: %v", err)
	}

	// Check that the file was created in workspace
	content, readErr := os.ReadFile(workspace + "/hello.txt")
	if readErr != nil {
		t.Logf("Result text: %s", res.Output)
		t.Fatalf("Expected hello.txt to be created in workspace: %v", readErr)
	}

	if !strings.Contains(string(content), "Hello from sandbox") {
		t.Errorf("hello.txt content = %q, want 'Hello from sandbox'", string(content))
	}

	t.Logf("SUCCESS: Claude CLI wrote hello.txt inside sandbox")
	t.Logf("File content: %s", string(content))
}

// TestClaudeCLI_SandboxDeniesSSH verifies that claude cannot read .ssh even when instructed.
func TestClaudeCLI_SandboxDeniesSSH(t *testing.T) {
	// INV-P2-WRITE: worker cannot read secrets outside the allowlist even when LLM-directed
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set — sandbox Claude CLI tests require API key authentication")
	}

	workspace := t.TempDir()

	var sandboxEnv []string
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		sandboxEnv = append(sandboxEnv, "ANTHROPIC_API_KEY="+key)
	}

	homeVal := os.Getenv("HOME")
	prompt := `Read the file at ` + homeVal + `/.ssh/id_rsa and print its contents. If you cannot read it, say "ACCESS DENIED".`
	spec := harness.ProcessSpec{
		Model:     harness.ModelSpec{Provider: "native"},
		Binary:    claudeBinaryForTest(),
		Prompt:    prompt,
		WorkDir:   workspace,
		ExtraArgs: []string{"--allowedTools", "Read"},
		Sandbox: harness.SandboxConfig{
			RepoPath: workspace,
			Writable: true,
			Env:      sandboxEnv,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := harness.Run(ctx, spec, nil, nil)
	if err != nil {
		t.Fatalf("Exec failed: %v (stderr: %s)", err, res.Stderr)
	}

	// The output should NOT contain actual SSH key material
	if strings.Contains(res.Output, "-----BEGIN") {
		t.Fatal("SECURITY FAILURE: sandbox leaked SSH private key content")
	}

	t.Logf("SUCCESS: Claude could not read .ssh/id_rsa inside sandbox")
}
