package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/testutil"
)

// --- Planner.Run tests ---

func TestPlanner_Run_Success(t *testing.T) {
	testutil.MustTempHome(t)
	sessionID := "planner-run-success"
	planMD := "# Plan\n\n## Goal\nBuild a thing.\n\n## Work Packages\n\n### 1. Do stuff\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"
	testutil.SetupPlanFile(t, sessionID, planMD)

	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "plan saved", SessionID: sessionID}}}
	p := NewPlanner(runner, "sys")
	result, err := p.Run(context.Background(), "build something", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Plan != planMD {
		t.Errorf("Plan mismatch:\ngot:  %q\nwant: %q", result.Plan, planMD)
	}
	if result.Chat != "plan saved" {
		t.Errorf("Chat = %q, want %q", result.Chat, "plan saved")
	}
	if result.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", result.SessionID, sessionID)
	}
}

func TestPlanner_Run_HardFailOnMissingPlanFileAndEmptyOutput(t *testing.T) {
	testutil.MustTempHome(t)

	// Stream output is empty AND plan file doesn't exist → hard error.
	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "", SessionID: "nonexistent-session-99"}}}
	p := NewPlanner(runner, "sys")
	_, err := p.Run(context.Background(), "prompt", nil)
	if err == nil {
		t.Fatal("expected hard error when plan file is missing and stream output is empty")
	}
}

func TestPlanner_Run_StreamFallbackWhenPlanFileMissing(t *testing.T) {
	testutil.MustTempHome(t)

	// Plan file doesn't exist, but stream result has content → fallback.
	streamReport := "## Critic Report\n\nNo blockers found."
	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: streamReport, SessionID: "fallback-session-1"}}}
	p := NewPlanner(runner, "sys")
	result, err := p.Run(context.Background(), "prompt", nil)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if result.Plan != streamReport {
		t.Errorf("Plan mismatch:\ngot:  %q\nwant: %q", result.Plan, streamReport)
	}
	if !result.StreamFallback {
		t.Error("expected StreamFallback to be true")
	}
	if result.Chat != streamReport {
		t.Errorf("Chat = %q, want %q", result.Chat, streamReport)
	}
	if result.SessionID != "fallback-session-1" {
		t.Errorf("SessionID = %q, want %q", result.SessionID, "fallback-session-1")
	}
}

func TestPlanner_Run_HardFailOnRunnerError(t *testing.T) {
	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Err: fmt.Errorf("connection refused")}}}
	p := NewPlanner(runner, "sys")
	_, err := p.Run(context.Background(), "prompt", nil)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected runner error propagation, got: %v", err)
	}
}

func TestPlanner_Run_NoSessionID(t *testing.T) {
	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "done"}}}
	p := NewPlanner(runner, "sys")
	_, err := p.Run(context.Background(), "prompt", nil)
	if err == nil {
		t.Fatal("expected error when runner returns no session ID")
	}
	if !strings.Contains(err.Error(), "no session ID") {
		t.Errorf("expected 'no session ID' error, got: %v", err)
	}
}

// --- Planner.Continue tests ---

func TestPlanner_Continue_ReadsPlanFile(t *testing.T) {
	testutil.MustTempHome(t)
	sessionID := "planner-continue-plan"
	planMD := "# Plan\n\n## Goal\nRevised.\n\n## Work Packages\n\n### 1. Updated"
	testutil.SetupPlanFile(t, sessionID, planMD)

	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "chat reply", SessionID: sessionID}}}
	p := NewPlanner(runner, "sys")
	result, err := p.Continue(context.Background(), sessionID, "update the plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Plan != planMD {
		t.Errorf("Plan mismatch:\ngot:  %q\nwant: %q", result.Plan, planMD)
	}
	if result.Chat != "chat reply" {
		t.Errorf("Chat = %q, want %q", result.Chat, "chat reply")
	}
}

func TestPlanner_Continue_ToleratesMissingPlanFile(t *testing.T) {
	// Chat-only continuation: runner returns a session ID but no plan file on disk.
	testutil.MustTempHome(t)

	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "here is my answer", SessionID: "chat-only-session"}}}
	p := NewPlanner(runner, "sys")
	result, err := p.Continue(context.Background(), "chat-only-session", "what is X?", nil)
	if err != nil {
		t.Fatalf("Continue must not return error for missing plan file, got: %v", err)
	}
	if result.Plan != "" {
		t.Errorf("Plan should be empty on chat-only continuation, got: %q", result.Plan)
	}
	if result.Chat != "here is my answer" {
		t.Errorf("Chat = %q, want %q", result.Chat, "here is my answer")
	}
}

func TestPlanner_Continue_RunnerError(t *testing.T) {
	runner := &testutil.FakeRunner{Calls: []testutil.FakeCall{{Err: fmt.Errorf("timeout")}}}
	p := NewPlanner(runner, "sys")
	_, err := p.Continue(context.Background(), "sid", "prompt", nil)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected runner error propagation, got: %v", err)
	}
}

// --- DetectPlanRevision tests ---

func TestDetectPlanRevision_ReturnsRevision(t *testing.T) {
	baseline := "# Plan\n\n## Goal\nOld"
	current := baseline
	newContent := "# Plan\n\n## Goal\nNew"

	result := DetectPlanRevision(newContent, baseline, nil, current)
	if result == nil {
		t.Fatal("expected non-nil revision")
	}
	if result.Markdown != newContent {
		t.Errorf("Markdown = %q, want %q", result.Markdown, newContent)
	}
}

func TestDetectPlanRevision_NoRevisionWhenUnchanged(t *testing.T) {
	content := "# Plan\n\n## Goal\nSame"
	result := DetectPlanRevision(content, content, nil, content)
	if result != nil {
		t.Errorf("expected nil when content matches baseline, got: %+v", result)
	}
}

func TestDetectPlanRevision_NilWhenPlanEmpty(t *testing.T) {
	result := DetectPlanRevision("", "baseline", nil, "current")
	if result != nil {
		t.Errorf("expected nil when planContent is empty, got: %+v", result)
	}
}

func TestDetectPlanRevision_EchoSuppressed(t *testing.T) {
	// baseline differs from current (e.g. baseline unavailable, so falls back to current)
	current := "# Plan\n\n## Goal\nUser edit"
	// Plan content equals current — echo suppression should return nil.
	result := DetectPlanRevision(current, "different-baseline", fmt.Errorf("no baseline"), current)
	if result != nil {
		t.Errorf("expected nil for echo suppression, got: %+v", result)
	}
}

func TestDetectPlanRevision_UsesBaselineWhenAvailable(t *testing.T) {
	baseline := "# Plan\n\n## Goal\nBase"
	current := "# Plan\n\n## Goal\nUser tweak" // user edited after baseline
	newContent := "# Plan\n\n## Goal\nArchitect revised"

	result := DetectPlanRevision(newContent, baseline, nil, current)
	if result == nil {
		t.Fatal("expected revision when content differs from baseline")
	}
}

// --- CheckPromptIntegrity tests ---

func TestCheckPromptIntegrity_NoTrip(t *testing.T) {
	assembled := "do the thing: user prompt here"
	out, tripped := CheckPromptIntegrity(assembled, "user prompt here")
	if tripped {
		t.Error("expected no canary trip")
	}
	if out != assembled {
		t.Errorf("expected unchanged assembled, got: %q", out)
	}
}

func TestCheckPromptIntegrity_Trips(t *testing.T) {
	assembled := "do the thing with something else entirely"
	orig := "user prompt here"
	out, tripped := CheckPromptIntegrity(assembled, orig)
	if !tripped {
		t.Error("expected canary to trip")
	}
	if !strings.Contains(out, orig) {
		t.Errorf("expected original prompt in fixed output, got: %q", out)
	}
	if !strings.Contains(out, "<original_prompt>") {
		t.Errorf("expected <original_prompt> wrapper, got: %q", out)
	}
}

func TestCheckPromptIntegrity_EmptyOriginalNoTrip(t *testing.T) {
	assembled := "some assembled text"
	out, tripped := CheckPromptIntegrity(assembled, "")
	if tripped {
		t.Error("empty originalPrompt must not trip canary")
	}
	if out != assembled {
		t.Errorf("expected unchanged assembled, got: %q", out)
	}
}

// --- Compile-time interface assertion ---

var _ harness.ContinuableRunner = (*testutil.FakeRunner)(nil)
