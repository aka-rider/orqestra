//go:build darwin && integration

package harness

import (
	"context"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeCLI_NoFallbackToCloud(t *testing.T) {
	// Verify opencode binary exists.
	binary := "/opt/homebrew/bin/opencode"
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("opencode binary not found at %s: %v", binary, err)
	}

	c := NewOpenCodeCLI(binary, WithOpenCodeModel("llama/qwen3.6-coder"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := c.RunPrint(ctx, "say hello in one word", "")
	if err != nil {
		t.Fatalf("RunPrint() error: %v", err)
	}

	// Assert: Output is non-empty.
	if result.Output == "" {
		t.Fatal("expected non-empty Output")
	}

	// Assert: SessionID is populated.
	if result.SessionID == "" {
		t.Fatal("expected non-empty SessionID")
	}

	// Assert: Usage is populated.
	if result.Usage.Total() == 0 {
		t.Fatal("expected non-zero Usage")
	}
}

func TestOpenCodeCLI_ModelRequired_FailClosed(t *testing.T) {
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// RunPrint should fail BEFORE spawning the opencode process.
	_, err := c.RunPrint(ctx, "say hello", "")
	if err == nil {
		t.Fatal("expected error for empty model")
	}

	// Error message should mention model is required.
	if !strings.Contains(err.Error(), "model is required") {
		t.Errorf("error should mention 'model is required', got: %v", err)
	}
}

func TestOpenCodeCLI_RunStreaming_PlanMode(t *testing.T) {
	binary := "/opt/homebrew/bin/opencode"
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("opencode binary not found at %s: %v", binary, err)
	}

	c := NewOpenCodeCLI(binary,
		WithOpenCodeModel("llama/qwen3.6-coder"),
		WithOpenCodeAgent("plan"),
	)

	events := make(chan StreamUpdate, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result, err := c.RunStreaming(ctx, "Write a plan for a hello-world Go app", "", events)
	if err != nil {
		t.Fatalf("RunStreaming() error: %v", err)
	}

	// Assert: Output is non-empty (plan text from text events).
	if result.Output == "" {
		t.Fatal("expected non-empty Output")
	}

	// Assert: SessionID is populated.
	if result.SessionID == "" {
		t.Fatal("expected non-empty SessionID")
	}

	// Assert: Usage is populated.
	if result.Usage.Total() == 0 {
		t.Fatal("expected non-zero Usage")
	}

	// Assert: At least one Text event was received.
	foundText := false
	drainText:
	for {
		select {
		case e := <-events:
			if e.Text != "" {
				foundText = true
				break drainText
			}
		default:
			break drainText
		}
	}
	if !foundText {
		t.Error("expected at least one StreamUpdate event of type Text")
	}

	// Assert: At least one TokenUsage event was received.
	foundUsage := false
	drainUsage:
	for {
		select {
		case e := <-events:
			if e.UsageValid {
				foundUsage = true
				break drainUsage
			}
		default:
			break drainUsage
		}
	}
	if !foundUsage {
		t.Error("expected at least one StreamUpdate event of type TokenUsage")
	}
}

func TestOpenCodeCLI_RunStreaming_BuildMode(t *testing.T) {
	binary := "/opt/homebrew/bin/opencode"
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("opencode binary not found at %s: %v", binary, err)
	}

	// Create a unique temp file path.
	tmpFile := "/tmp/opencode-test-build-" + randomString(8) + ".txt"
	defer os.Remove(tmpFile) // cleanup

	prompt := "Create a file " + tmpFile + " containing 'hello'"

	c := NewOpenCodeCLI(binary,
		WithOpenCodeModel("llama/qwen3.6-coder"),
		WithOpenCodeAgent("build"),
	)

	events := make(chan StreamUpdate, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result, err := c.RunStreaming(ctx, prompt, "", events)
	if err != nil {
		t.Fatalf("RunStreaming() error: %v", err)
	}

	// Assert: Output is non-empty.
	if result.Output == "" {
		t.Fatal("expected non-empty Output")
	}

	// Assert: SessionID is populated.
	if result.SessionID == "" {
		t.Fatal("expected non-empty SessionID")
	}

	// Assert: Usage is populated.
	if result.Usage.Total() == 0 {
		t.Fatal("expected non-zero Usage")
	}

	// Assert: At least one ToolUse event was received.
	foundTool := false
	drainTool:
	for {
		select {
		case e := <-events:
			if e.Tool != "" {
				foundTool = true
				break drainTool
			}
		default:
			break drainTool
		}
	}
	if !foundTool {
		t.Error("expected at least one StreamUpdate event of type ToolUse")
	}

	// Assert: The file exists and contains expected content.
	content, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error: %v", tmpFile, err)
	}
	if !strings.Contains(string(content), "hello") {
		t.Errorf("file content = %q, want it to contain 'hello'", string(content))
	}
}

func TestOpenCodeCLI_RunContinue_SessionResume(t *testing.T) {
	binary := "/opt/homebrew/bin/opencode"
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("opencode binary not found at %s: %v", binary, err)
	}

	c := NewOpenCodeCLI(binary,
		WithOpenCodeModel("llama/qwen3.6-coder"),
	)

	// First call: create a session.
	events1 := make(chan StreamUpdate, 100)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel1()

	result1, err := c.RunStreaming(ctx1, "my name is test", "", events1)
	if err != nil {
		t.Fatalf("first RunStreaming() error: %v", err)
	}
	if result1.SessionID == "" {
		t.Fatal("expected non-empty SessionID from first call")
	}

	sessionID := result1.SessionID

	// Second call: continue the session.
	events2 := make(chan StreamUpdate, 100)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()

	result2, err := c.RunContinue(ctx2, sessionID, "what is my name", events2)
	if err != nil {
		t.Fatalf("RunContinue() error: %v", err)
	}

	// Assert: SessionID matches the original session ID.
	if result2.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", result2.SessionID, sessionID)
	}

	// Assert: Output is non-empty (continuation response).
	if result2.Output == "" {
		t.Fatal("expected non-empty Output from continuation")
	}

	// Assert: Usage is populated.
	if result2.Usage.Total() == 0 {
		t.Fatal("expected non-zero Usage from continuation")
	}
}

func TestOpenCodeCLI_PureModeNoMCP(t *testing.T) {
	binary := "/opt/homebrew/bin/opencode"
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("opencode binary not found at %s: %v", binary, err)
	}

	c := NewOpenCodeCLI(binary,
		WithOpenCodeModel("llama/qwen3.6-coder"),
		WithOpenCodePure(true),
	)

	events := make(chan StreamUpdate, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := c.RunStreaming(ctx, "say hello", "", events)
	if err != nil {
		t.Fatalf("RunStreaming() error: %v", err)
	}

	// Assert: No ToolUse events with tool name containing "mcp" or "plugin".
	// Drain buffered events (non-blocking) since the channel is never closed.
	drainEvents:
	for {
		select {
		case e := <-events:
			if e.Tool != "" {
				toolLower := strings.ToLower(e.Tool)
				if strings.Contains(toolLower, "mcp") || strings.Contains(toolLower, "plugin") {
					t.Errorf("unexpected MCP/plugin tool use: %q", e.Tool)
				}
			}
		default:
			break drainEvents
		}
	}

	// Assert: Output is non-empty.
	if result.Output == "" {
		t.Fatal("expected non-empty Output")
	}

	// Assert: Usage is populated.
	if result.Usage.Total() == 0 {
		t.Fatal("expected non-zero Usage")
	}
}

func TestOpenCodeCLI_AgentPlanVsBuild(t *testing.T) {
	binary := "/opt/homebrew/bin/opencode"
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("opencode binary not found at %s: %v", binary, err)
	}

	// Plan mode: should produce text output without tool_use events.
	cPlan := NewOpenCodeCLI(binary,
		WithOpenCodeModel("llama/qwen3.6-coder"),
		WithOpenCodeAgent("plan"),
	)

	eventsPlan := make(chan StreamUpdate, 100)
	ctxPlan, cancelPlan := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelPlan()

	resultPlan, err := cPlan.RunStreaming(ctxPlan, "write a plan for a hello world app", "", eventsPlan)
	if err != nil {
		t.Fatalf("plan mode RunStreaming() error: %v", err)
	}
	if resultPlan.Output == "" {
		t.Fatal("plan mode: expected non-empty Output")
	}

	// Build mode: should produce tool_use events.
	tmpFile := "/tmp/opencode-test-agent-" + randomString(8) + ".txt"
	defer os.Remove(tmpFile)

	cBuild := NewOpenCodeCLI(binary,
		WithOpenCodeModel("llama/qwen3.6-coder"),
		WithOpenCodeAgent("build"),
	)

	eventsBuild := make(chan StreamUpdate, 100)
	ctxBuild, cancelBuild := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelBuild()

	prompt := "Create a file " + tmpFile + " containing 'agent test'"
	resultBuild, err := cBuild.RunStreaming(ctxBuild, prompt, "", eventsBuild)
	if err != nil {
		t.Fatalf("build mode RunStreaming() error: %v", err)
	}
	if resultBuild.Output == "" {
		t.Fatal("build mode: expected non-empty Output")
	}

	// Verify build mode produced tool_use events.
	foundTool := false
	drainAgentTool:
	for {
		select {
		case e := <-eventsBuild:
			if e.Tool != "" {
				foundTool = true
				break drainAgentTool
			}
		default:
			break drainAgentTool
		}
	}
	if !foundTool {
		t.Error("build mode: expected at least one ToolUse event")
	}
}

// randomString generates a random alphanumeric string of the given length.
func randomString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
