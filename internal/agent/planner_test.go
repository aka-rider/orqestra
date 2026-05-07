package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// plannerMockCLIRunner is a test double for the CLIRunner interface.
type plannerMockCLIRunner struct {
	response string
	err      error
}

func (m *plannerMockCLIRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func (m *plannerMockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func TestPlan_Success(t *testing.T) {
	specData := map[string]any{
		"goal":       "Build a REST API",
		"steps":      []string{"Create main.go", "Add handler", "Write tests"},
		"acceptance": []string{"Server starts", "GET /health returns 200"},
	}
	specJSON, _ := json.Marshal(specData)
	mock := &plannerMockCLIRunner{response: string(specJSON)}

	cfg := &config.PlannerConfig{
		ModelRef:     "test-model",
		SystemPrompt: "Plan mode.",
	}

	p := NewPlanner(mock, cfg)
	po, err := p.Plan(context.Background(), "build a REST API")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if po.Spec.Goal != "Build a REST API" {
		t.Errorf("goal = %q, want %q", po.Spec.Goal, "Build a REST API")
	}
	if len(po.Spec.Steps) != 3 {
		t.Errorf("steps = %d, want 3", len(po.Spec.Steps))
	}
}

func TestPlan_JsonEnvelope(t *testing.T) {
	specData := map[string]any{
		"goal":       "Refactor auth",
		"steps":      []string{"Extract interface", "Add tests"},
		"acceptance": []string{"Tests pass"},
	}
	specJSON, _ := json.Marshal(specData)
	envelope := fmt.Sprintf(`{"type":"result","result":%q}`, string(specJSON))
	mock := &plannerMockCLIRunner{response: envelope}

	cfg := &config.PlannerConfig{ModelRef: "test-model", SystemPrompt: "Plan."}
	p := NewPlanner(mock, cfg)
	po, err := p.Plan(context.Background(), "refactor auth")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if po.Spec.Goal != "Refactor auth" {
		t.Errorf("goal = %q, want %q", po.Spec.Goal, "Refactor auth")
	}
}

func TestPlan_InvalidJSON(t *testing.T) {
	mock := &plannerMockCLIRunner{response: "not json at all"}

	cfg := &config.PlannerConfig{ModelRef: "test-model", SystemPrompt: "Plan."}
	p := NewPlanner(mock, cfg)
	_, err := p.Plan(context.Background(), "do something")

	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestPlan_IncompleteSpec(t *testing.T) {
	specData := map[string]any{"goal": "Missing steps and acceptance"}
	specJSON, _ := json.Marshal(specData)
	mock := &plannerMockCLIRunner{response: string(specJSON)}

	cfg := &config.PlannerConfig{ModelRef: "test-model", SystemPrompt: "Plan."}
	p := NewPlanner(mock, cfg)
	_, err := p.Plan(context.Background(), "incomplete")

	if err == nil {
		t.Fatal("expected error for incomplete spec")
	}
}

func TestPlan_MarkdownFencedJSON(t *testing.T) {
	specData := map[string]any{
		"goal":       "Deploy app",
		"steps":      []string{"Build image", "Push to registry"},
		"acceptance": []string{"Container runs"},
	}
	specJSON, _ := json.Marshal(specData)
	fenced := "```json\n" + string(specJSON) + "\n```"
	mock := &plannerMockCLIRunner{response: fenced}

	cfg := &config.PlannerConfig{ModelRef: "test-model", SystemPrompt: "Plan."}
	p := NewPlanner(mock, cfg)
	po, err := p.Plan(context.Background(), "deploy")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if po.Spec.Goal != "Deploy app" {
		t.Errorf("goal = %q, want %q", po.Spec.Goal, "Deploy app")
	}
}

func TestParseSpec_MarkdownFencedJSON(t *testing.T) {
	spec := map[string]any{
		"goal":       "Test fences",
		"steps":      []string{"Step one"},
		"acceptance": []string{"Done"},
	}
	specJSON, _ := json.Marshal(spec)
	fenced := "```json\n" + string(specJSON) + "\n```"

	cfg := &config.PlannerConfig{ModelRef: "test-model", SystemPrompt: "Plan."}
	p := NewPlanner(&plannerMockCLIRunner{}, cfg)
	parsed, err := p.ParseSpec(fenced)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Goal != "Test fences" {
		t.Errorf("goal = %q, want %q", parsed.Goal, "Test fences")
	}
}
