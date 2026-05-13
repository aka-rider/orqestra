//go:build e2e

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// e2eResolvedModel returns a ResolvedModel from environment variables.
func e2eResolvedModel() config.ResolvedModel {
	baseURL := os.Getenv("ORQESTRA_LLM_URL")
	if baseURL == "" {
		baseURL = "http://192.168.50.212:11434"
	}
	model := os.Getenv("ORQESTRA_LLM_MODEL")
	if model == "" {
		model = "qwen3.6"
	}
	return config.ResolvedModel{
		BaseURL: baseURL,
		Model:   model,
	}
}

// e2eArchitectRunner creates a ClaudeCLI runner configured for architect-style testing.
func e2eArchitectRunner() *harness.ClaudeCLI {
	return harness.NewClaudeCLI(
		e2eResolvedModel(),
		harness.WithAllowedTools([]string{"Read", "Grep", "Glob", "Bash"}),
		harness.WithDisallowedTools([]string{"ExitPlanMode"}),
		harness.WithPermissionMode("plan"),
	)
}

// countJSONLUserTurns counts lines with "type":"user" in a JSONL file.
func countJSONLUserTurns(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read JSONL: %v", err)
	}
	count := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["type"] == "user" {
			count++
		}
	}
	return count
}

func TestResume_ToolRestrictionsPreserved(t *testing.T) {
	runner := e2eArchitectRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var buf bytes.Buffer

	// First call: establish session
	result1, err := runner.RunStreaming(ctx, "List the files in the current directory", "You are a test agent.", &buf)
	if err != nil {
		t.Fatalf("first RunStreaming failed: %v", err)
	}
	sessionID := result1.SessionID
	if sessionID == "" {
		t.Fatal("expected non-empty session ID from first call")
	}
	t.Logf("First call session ID: %s", sessionID)

	// Second call: resume session
	buf.Reset()
	result2, err := runner.RunContinue(ctx, sessionID, "What tools do you have available? List them all.", &buf)
	if err != nil {
		t.Fatalf("RunContinue failed: %v", err)
	}
	if result2.Output == "" {
		t.Fatal("expected non-empty output from RunContinue")
	}
	t.Logf("RunContinue output length: %d chars", len(result2.Output))

	// Inspect JSONL for user turns
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	jsonlPath, err := harness.ResolveSessionLogPath(cwd, sessionID)
	if err != nil {
		t.Logf("WARNING: could not resolve session log (may not exist for this provider): %v", err)
	} else {
		turns := countJSONLUserTurns(t, jsonlPath)
		t.Logf("JSONL user turns: %d (expected 2)", turns)
		if turns < 2 {
			t.Errorf("expected at least 2 user turns in JSONL, got %d", turns)
		}
	}

	// Informational: log whether model mentions Write/Edit tools
	output := strings.ToLower(result2.Output)
	if strings.Contains(output, "write") || strings.Contains(output, "edit") {
		t.Logf("INFO: model response mentions Write/Edit tools (extraArgs enforce restrictions)")
	} else {
		t.Logf("INFO: model response does NOT mention Write/Edit tools")
	}
}

func TestResume_ChatOnlyResponse(t *testing.T) {
	runner := e2eArchitectRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	var buf bytes.Buffer

	// First call: produce a plan
	architectSystemPrompt := "You are a Principal Engineer. Produce a short implementation plan starting with '# Plan' and containing '## Work Packages'. Keep it brief — 2 work packages max."
	trivialPrompt := "Create a plan for adding a /health endpoint to a Go HTTP server."

	result1, err := runner.RunStreaming(ctx, trivialPrompt, architectSystemPrompt, &buf)
	if err != nil {
		t.Fatalf("first RunStreaming failed: %v", err)
	}
	sessionID := result1.SessionID
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Verify first result is a valid plan
	a := &Architect{}
	_, _, _, parseErr := a.parsePlanResult(result1)
	if parseErr != nil {
		t.Logf("WARNING: first call did not produce parseable plan (may be provider-specific): %v", parseErr)
	}

	// Second call: ask a question (should get chat-only response)
	buf.Reset()
	continuePrompt := `The current implementation plan is below. The reviewer sent a message.

<current_plan>
` + result1.Output + `
</current_plan>

<reviewer_message>
Why did you choose this approach?
</reviewer_message>

If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
If the reviewer requests changes, revise the plan and output the complete updated plan.
Begin with your response. Then, ONLY if you changed the plan, output the full revised plan starting with "# Plan".
Do NOT output "# Plan" unless you actually changed the plan.`

	result2, err := runner.RunContinue(ctx, sessionID, continuePrompt, &buf)
	if err != nil {
		t.Fatalf("RunContinue failed: %v", err)
	}

	if result2.Output == "" {
		t.Fatal("expected non-empty response to question")
	}

	// Chat-only response should NOT contain "# Plan" at the start
	trimmed := strings.TrimSpace(result2.Output)
	if strings.HasPrefix(trimmed, "# Plan") {
		t.Logf("WARNING: model output starts with '# Plan' for a question — may need prompt tuning")
	} else {
		t.Logf("OK: chat-only response does not start with '# Plan'")
	}

	t.Logf("Chat response length: %d chars", len(result2.Output))
}

func TestResume_PlanRevisionResponse(t *testing.T) {
	runner := e2eArchitectRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	var buf bytes.Buffer

	// First call: produce a plan
	architectSystemPrompt := "You are a Principal Engineer. Produce a short implementation plan starting with '# Plan' and containing '## Work Packages'. Keep it brief — 2 work packages max."
	trivialPrompt := "Create a plan for adding a /health endpoint and a /ready endpoint to a Go HTTP server."

	result1, err := runner.RunStreaming(ctx, trivialPrompt, architectSystemPrompt, &buf)
	if err != nil {
		t.Fatalf("first RunStreaming failed: %v", err)
	}
	sessionID := result1.SessionID
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	originalOutput := result1.Output

	// Second call: request plan revision
	buf.Reset()
	revisionPrompt := `The current implementation plan is below. The reviewer sent a message.

<current_plan>
` + originalOutput + `
</current_plan>

<reviewer_message>
Remove the first work package entirely and output the complete revised plan starting with "# Plan".
</reviewer_message>

If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
If the reviewer requests changes, revise the plan and output the complete updated plan.
Begin with your response. Then, ONLY if you changed the plan, output the full revised plan starting with "# Plan".
Do NOT output "# Plan" unless you actually changed the plan.`

	result2, err := runner.RunContinue(ctx, sessionID, revisionPrompt, &buf)
	if err != nil {
		t.Fatalf("RunContinue failed: %v", err)
	}

	if result2.Output == "" {
		t.Fatal("expected non-empty revision output")
	}

	// Revision response should contain a plan
	if !strings.Contains(result2.Output, "# Plan") {
		t.Errorf("expected revised output to contain '# Plan'")
	}

	// The revised plan should differ from the original
	if strings.TrimSpace(result2.Output) == strings.TrimSpace(originalOutput) {
		t.Error("expected revised plan to differ from original")
	}

	t.Logf("Revision output length: %d chars (original: %d)", len(result2.Output), len(originalOutput))
}

func TestContinuePrompt_Variants(t *testing.T) {
	runner := e2eArchitectRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	var buf bytes.Buffer

	// First call: produce a plan
	architectSystemPrompt := "You are a Principal Engineer. Produce a short implementation plan starting with '# Plan' and containing '## Work Packages'. Include exactly 3 work packages."
	trivialPrompt := "Create a plan for building a CLI tool with three subcommands: init, run, and status."

	result1, err := runner.RunStreaming(ctx, trivialPrompt, architectSystemPrompt, &buf)
	if err != nil {
		t.Fatalf("initial plan failed: %v", err)
	}
	sessionID := result1.SessionID
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}
	originalPlan := result1.Output

	buildContinuePrompt := func(plan, message string) string {
		return `The current implementation plan is below. The reviewer sent a message.

<current_plan>
` + plan + `
</current_plan>

<reviewer_message>
` + message + `
</reviewer_message>

If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
If the reviewer requests changes, revise the plan and output the complete updated plan.
Begin with your response. Then, ONLY if you changed the plan, output the full revised plan starting with "# Plan".
Do NOT output "# Plan" unless you actually changed the plan.`
	}

	t.Run("QuestionGetsAnswer", func(t *testing.T) {
		buf.Reset()
		prompt := buildContinuePrompt(originalPlan, "Why did you choose to put step 3 before step 4?")
		result, err := runner.RunContinue(ctx, sessionID, prompt, &buf)
		if err != nil {
			t.Fatalf("RunContinue failed: %v", err)
		}
		if result.Output == "" {
			t.Fatal("expected non-empty response")
		}
		trimmed := strings.TrimSpace(result.Output)
		if strings.HasPrefix(trimmed, "# Plan") {
			t.Error("question should NOT produce a plan output")
		}
		t.Logf("Question response (%d chars): %s", len(result.Output), truncateForLog(result.Output, 200))
	})

	t.Run("ChangeRequestGetsPlan", func(t *testing.T) {
		buf.Reset()
		prompt := buildContinuePrompt(originalPlan, "Remove work package 1 and renumber the remaining packages")
		result, err := runner.RunContinue(ctx, sessionID, prompt, &buf)
		if err != nil {
			t.Fatalf("RunContinue failed: %v", err)
		}
		if result.Output == "" {
			t.Fatal("expected non-empty response")
		}
		if !strings.Contains(result.Output, "# Plan") {
			t.Error("change request should produce a plan output containing '# Plan'")
		}
		if !strings.Contains(result.Output, "## Work Packages") {
			t.Error("change request should produce output containing '## Work Packages'")
		}
		t.Logf("Change response (%d chars): %s", len(result.Output), truncateForLog(result.Output, 200))
	})

	t.Run("AmbiguousRequestStillWorks", func(t *testing.T) {
		buf.Reset()
		prompt := buildContinuePrompt(originalPlan, "The verification section is weak")
		result, err := runner.RunContinue(ctx, sessionID, prompt, &buf)
		if err != nil {
			t.Fatalf("RunContinue failed: %v", err)
		}
		if result.Output == "" {
			t.Fatal("expected non-empty response")
		}
		// Accept either outcome — the model might answer or revise.
		t.Logf("Ambiguous response (%d chars, has plan: %v): %s",
			len(result.Output),
			strings.Contains(result.Output, "# Plan"),
			truncateForLog(result.Output, 200))
	})
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
