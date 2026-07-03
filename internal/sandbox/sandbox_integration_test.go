//go:build darwin && integration

package sandbox_test

import (
	"os/exec"

	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/sandbox"
	"github.com/xiii/orqestra/internal/sandbox/detect"
)

// TestClaudeCLI_InSandbox verifies that claude CLI can run inside the seatbelt sandbox.
// This test requires:
// - claude CLI installed and authenticated
// - ANTHROPIC_API_KEY or valid OAuth session
// Run with: go test ./internal/sandbox/ -tags integration -run TestClaudeCLI_InSandbox -v
func TestClaudeCLI_InSandbox(t *testing.T) {
	// INV-P2-WRITE: claude CLI runs and writes files inside the seatbelt sandbox (L3 live test)
	// Check claude is available
	claudeBinary := "claude"
	if bin := os.Getenv("CLAUDE_BINARY"); bin != "" {
		claudeBinary = bin
	}

	// Skip when ANTHROPIC_API_KEY is not set — the test requires API access
	// and Claude CLI will fail with 401 otherwise. OAuth auth is harder to
	// detect programmatically; CI/CD pipelines typically use API keys.
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set — sandbox Claude CLI tests require API key authentication")
	}

	workspace := t.TempDir()

	// Build sandbox with API key if available, via HarnessEnv (the same
	// key=value channel harness.Run uses for model routing env — ExtraEnv was
	// Tier-A dead code, never wired from config to the sandbox).
	var harnessEnv []string
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		harnessEnv = append(harnessEnv, "ANTHROPIC_API_KEY="+key)
	}

	homeEnv := os.Getenv("HOME")
	claudeSnap, err := detect.DetectClaude(homeEnv, claudeBinary)
	if err != nil {
		t.Fatalf("DetectClaude failed: %v", err)
	}

	sb, err := sandbox.New(sandbox.Config{
		RepoPath: workspace, RepoWritable: true,
		HarnessEnv: harnessEnv,
		Profiles:   []sandbox.Snapshot{claudeSnap},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Run claude with a simple prompt that requires tool usage (file write)
	prompt := `Create a file called hello.txt in the current directory containing exactly "Hello from sandbox". Use the file write tool. Do not ask for confirmation.`
	command := []string{
		claudeBinary,
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--allowedTools", "Write",
	}

	t.Logf("Running: %v", command)
	t.Logf("Workspace: %s", workspace)

	exitCode, err := execSandbox(sb, ctx, command, &stdout)
	t.Logf("Exit code: %d", exitCode)
	t.Logf("Stdout length: %d bytes", stdout.Len())
	t.Logf("Stdout text: %s", stdout.String())
	if stdout.Len() == 0 && exitCode != 0 {
		t.Logf("Claude likely hit a startup error (exit %d, no stdout). Check ANTHROPIC_API_KEY and ~/.claude auth.", exitCode)
	}

	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	// Parse stream-json output to check for tool usage
	lines := strings.Split(stdout.String(), "\n")
	var toolUseFound bool
	var resultText string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event["type"] == "content_block_start" {
			if cb, ok := event["content_block"].(map[string]interface{}); ok {
				if cb["type"] == "tool_use" {
					toolUseFound = true
					t.Logf("Tool use detected: %v", cb["name"])
				}
			}
		}
		if event["type"] == "result" {
			if r, ok := event["result"].(string); ok {
				resultText = r
			}
		}
	}

	if exitCode != 0 {
		// Print first 2000 chars of output for debugging
		out := stdout.String()
		if len(out) > 2000 {
			out = out[:2000]
		}
		t.Fatalf("claude CLI exited with code %d.\nOutput:\n%s", exitCode, out)
	}

	// Check that the file was created in workspace
	content, err := os.ReadFile(workspace + "/hello.txt")
	if err != nil {
		t.Logf("Result text: %s", resultText)
		t.Logf("Tool use found: %v", toolUseFound)
		t.Fatalf("Expected hello.txt to be created in workspace: %v", err)
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
	// Skip when ANTHROPIC_API_KEY is not set — the test requires API access.
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set — sandbox Claude CLI tests require API key authentication")
	}

	claudeBinary := "claude"
	if bin := os.Getenv("CLAUDE_BINARY"); bin != "" {
		claudeBinary = bin
	}

	workspace := t.TempDir()
	var harnessEnv []string
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		harnessEnv = append(harnessEnv, "ANTHROPIC_API_KEY="+key)
	}

	homeEnv := os.Getenv("HOME")
	claudeSnap, err := detect.DetectClaude(homeEnv, claudeBinary)
	if err != nil {
		t.Fatalf("DetectClaude failed: %v", err)
	}

	sb, err := sandbox.New(sandbox.Config{
		RepoPath: workspace, RepoWritable: true,
		HarnessEnv: harnessEnv,
		Profiles:   []sandbox.Snapshot{claudeSnap},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	homeVal := os.Getenv("HOME")
	prompt := `Read the file at ` + homeVal + `/.ssh/id_rsa and print its contents. If you cannot read it, say "ACCESS DENIED".`
	command := []string{
		claudeBinary,
		"--print",
		"-p", prompt,
		"--output-format", "json",
		"--allowedTools", "Read",
	}

	exitCode, err := execSandbox(sb, ctx, command, &stdout)
	t.Logf("Exit code: %d", exitCode)

	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	output := stdout.String()
	// The output should NOT contain actual SSH key material
	if strings.Contains(output, "-----BEGIN") {
		t.Fatal("SECURITY FAILURE: sandbox leaked SSH private key content")
	}

	t.Logf("SUCCESS: Claude could not read .ssh/id_rsa inside sandbox")
}

func execSandbox(sb *sandbox.Sandbox, ctx context.Context, command []string, stdout *bytes.Buffer) (int, error) {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = stdout
	if err := sb.Wrap(cmd); err != nil {
		return -1, err
	}
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), err
		}
		return -1, err
	}
	return 0, nil
}
