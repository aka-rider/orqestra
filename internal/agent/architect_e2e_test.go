//go:build e2e

package agent

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

func TestArchitectSessionContinuation(t *testing.T) {
	runner := e2eArchitectRunner()
	cfg := config.ArchitectConfig{
		Model:        e2eResolvedModel().Model,
		SystemPrompt: "You are a Principal Engineer. Produce a short implementation plan starting with '# Plan' and containing '## Work Packages'. Keep it brief — 2 work packages max.",
	}
	architect := NewArchitect(runner, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	var buf bytes.Buffer

	// Step 1: Produce initial plan via RefineStreaming
	simpleResearchDraft := `# Research Draft

## Findings
- The codebase has a Go HTTP server in cmd/server/main.go
- Currently no health endpoint exists
- The server uses standard net/http

## Recommendation
Add a /health endpoint that returns 200 OK with a JSON body.`

	plan, _, sessionID, err := architect.RefineStreaming(ctx, simpleResearchDraft, &buf)
	if err != nil {
		t.Fatalf("RefineStreaming failed: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID from RefineStreaming")
	}
	if plan.Markdown == "" {
		t.Fatal("expected non-empty plan markdown")
	}
	t.Logf("Initial plan produced (%d chars), session ID: %s", len(plan.Markdown), sessionID)

	// Write the plan to a temp file (ContinueSession reads from disk)
	planFile, err := os.CreateTemp(t.TempDir(), "plan-*.md")
	if err != nil {
		t.Fatalf("create temp plan file: %v", err)
	}
	if _, err := planFile.WriteString(plan.Markdown); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	planFile.Close()
	planPath := planFile.Name()

	// Step 2: Ask a question — expect chat-only response
	buf.Reset()
	chatResponse, chatUsage, err := architect.ContinueSession(ctx, sessionID, planPath, "Why this approach?", &buf)
	if err != nil {
		t.Fatalf("ContinueSession (question) failed: %v", err)
	}
	if chatResponse == "" {
		t.Fatal("expected non-empty chat response")
	}
	t.Logf("Chat response (%d chars): %s", len(chatResponse), truncateForLog(chatResponse, 200))
	t.Logf("Chat usage: input=%d output=%d", chatUsage.InputTokens, chatUsage.OutputTokens)

	// The plan file should not have changed (chat-only)
	planData, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("re-read plan: %v", err)
	}
	if string(planData) != plan.Markdown {
		t.Error("expected plan file unchanged after chat-only response")
	}

	// Step 3: Request revision — expect plan update
	buf.Reset()
	revResponse, revUsage, err := architect.ContinueSession(ctx, sessionID, planPath, "Remove the first work package entirely", &buf)
	if err != nil {
		t.Fatalf("ContinueSession (revision) failed: %v", err)
	}
	if revResponse == "" {
		t.Fatal("expected non-empty revision response")
	}
	t.Logf("Revision response (%d chars): %s", len(revResponse), truncateForLog(revResponse, 200))
	t.Logf("Revision usage: input=%d output=%d", revUsage.InputTokens, revUsage.OutputTokens)

	// Check whether the response contains a revised plan
	if strings.Contains(revResponse, "# Plan") {
		t.Logf("OK: revision response contains '# Plan' — model produced a revised plan")
	} else {
		t.Logf("INFO: revision response does not contain '# Plan' — model may have only described changes")
	}

	// Step 4: Inspect JSONL for user turn count
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	jsonlPath, err := harness.ResolveSessionLogPath(cwd, sessionID)
	if err != nil {
		t.Logf("WARNING: could not resolve session log: %v", err)
	} else {
		turns := countJSONLUserTurns(t, jsonlPath)
		t.Logf("JSONL user turns: %d (expected 3)", turns)
		if turns < 3 {
			t.Errorf("expected at least 3 user turns (initial + question + revision), got %d", turns)
		}
	}
}
