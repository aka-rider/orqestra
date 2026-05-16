package agent

import (
	"context"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/testutil"
)

func TestCritic_ReviewStreaming(t *testing.T) {
	report := `## Critic Report

### Blockers Found

#### 1. Wrong file path for config struct
- **Category**: Missing file / wrong path
- **Severity**: High
- **Evidence**: ` + "`internal/config/config.go`" + ` — the function NewConfig does not exist
- **Impact**: Worker will fail at step 2 when trying to call NewConfig()
- **Suggested fix**: Use LoadConfig() instead

#### 2. Missing test update
- **Category**: Dependency gap
- **Severity**: Medium
- **Evidence**: ` + "`internal/config/config_test.go`" + ` has 3 tests referencing the old API
- **Impact**: Tests will fail after implementation
- **Suggested fix**: Update test cases to use the new function signature

#### 3. Minor naming inconsistency
- **Category**: Scope creep or omission
- **Severity**: Low
- **Evidence**: Plan says "validator" but code uses "checker" everywhere
- **Impact**: Worker can resolve this independently
- **Suggested fix**: Use "checker" consistently

### Verified Claims
- ` + "`internal/agent/spec.go`" + ` exists and contains the RawPlan type
- ` + "`internal/harness/claude_cli.go`" + ` implements CLIRunner interface

### Summary
- Total blockers: 3 (1 high, 1 medium, 1 low)
- Overall assessment: Plan has one critical path issue that must be fixed before execution.`

	runner := &testutil.FakeRunner{
		Calls: []testutil.FakeCall{{
			Output:    report,
			SessionID: "sess-123",
			Usage:     harness.TokenUsage{InputTokens: 1000, OutputTokens: 500},
		}},
	}

	critic := NewCritic(runner, config.CriticConfig{BaseAgentConfig: config.BaseAgentConfig{
		Model:        "medium",
		SystemPrompt: "test system prompt",
	}})

	result, usage, sessionID, err := critic.ReviewStreaming(context.Background(), "user prompt", "# Plan\n...", io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sessionID != "sess-123" {
		t.Errorf("sessionID = %q, want %q", sessionID, "sess-123")
	}
	if usage.InputTokens != 1000 {
		t.Errorf("input tokens = %d, want 1000", usage.InputTokens)
	}

	if result.Blockers.High != 1 {
		t.Errorf("high blockers = %d, want 1", result.Blockers.High)
	}
	if result.Blockers.Medium != 1 {
		t.Errorf("medium blockers = %d, want 1", result.Blockers.Medium)
	}
	if result.Blockers.Low != 1 {
		t.Errorf("low blockers = %d, want 1", result.Blockers.Low)
	}
	if result.Blockers.Total() != 3 {
		t.Errorf("total blockers = %d, want 3", result.Blockers.Total())
	}
	if result.Markdown != report {
		t.Error("report markdown should be preserved verbatim")
	}
}

func TestCritic_ReviewStreaming_NoBlockers(t *testing.T) {
	report := `## Critic Report

### Blockers Found

None found.

### Verified Claims
- All file paths in the plan exist
- Function signatures match

### Summary
- Total blockers: 0 (0 high, 0 medium, 0 low)
- Overall assessment: Plan is ready for execution.`

	runner := &testutil.FakeRunner{
		Calls: []testutil.FakeCall{{Output: report}},
	}

	critic := NewCritic(runner, config.CriticConfig{BaseAgentConfig: config.BaseAgentConfig{SystemPrompt: "test"}})
	result, _, _, err := critic.ReviewStreaming(context.Background(), "prompt", "plan", io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Blockers.Total() != 0 {
		t.Errorf("total blockers = %d, want 0", result.Blockers.Total())
	}
}

func TestCritic_ReviewStreaming_Error(t *testing.T) {
	runner := &testutil.FakeRunner{
		Calls: []testutil.FakeCall{{Err: context.DeadlineExceeded}},
	}

	critic := NewCritic(runner, config.CriticConfig{BaseAgentConfig: config.BaseAgentConfig{SystemPrompt: "test"}})
	_, _, _, err := critic.ReviewStreaming(context.Background(), "prompt", "plan", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
}
