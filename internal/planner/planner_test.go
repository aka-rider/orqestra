package planner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/planner"
)

// mockCLIRunner is a test double for the CLIRunner interface.
type mockCLIRunner struct {
	response string
	err      error
}

func (m *mockCLIRunner) RunPrint(_ context.Context, _, _ string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func (m *mockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func TestPlan_Success(t *testing.T) {
	spec := map[string]any{
		"goal":       "Build a REST API",
		"steps":      []string{"Create main.go", "Add handler", "Write tests"},
		"acceptance": []string{"Server starts", "GET /health returns 200"},
	}
	specJSON, _ := json.Marshal(spec)
	mock := &mockCLIRunner{response: string(specJSON)}

	cfg := &config.PlannerConfig{
		Model:        "test-model",
		SystemPrompt: "Plan mode.",
	}

	p := planner.New(mock, cfg)
	result, err := p.Plan(context.Background(), "build a REST API")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsOk() {
		t.Fatalf("expected ok result, got: %v", result.Err)
	}
	if result.Value.Goal != "Build a REST API" {
		t.Errorf("goal = %q, want %q", result.Value.Goal, "Build a REST API")
	}
	if len(result.Value.Steps) != 3 {
		t.Errorf("steps = %d, want 3", len(result.Value.Steps))
	}
}

func TestPlan_JsonEnvelope(t *testing.T) {
	spec := map[string]any{
		"goal":       "Refactor auth",
		"steps":      []string{"Extract interface", "Add tests"},
		"acceptance": []string{"Tests pass"},
	}
	specJSON, _ := json.Marshal(spec)
	envelope := fmt.Sprintf(`{"type":"result","result":%q}`, string(specJSON))
	mock := &mockCLIRunner{response: envelope}

	cfg := &config.PlannerConfig{Model: "test-model", SystemPrompt: "Plan."}
	p := planner.New(mock, cfg)
	result, err := p.Plan(context.Background(), "refactor auth")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsOk() {
		t.Fatalf("expected ok, got: %v", result.Err)
	}
	if result.Value.Goal != "Refactor auth" {
		t.Errorf("goal = %q, want %q", result.Value.Goal, "Refactor auth")
	}
}

func TestPlan_InvalidJSON(t *testing.T) {
	mock := &mockCLIRunner{response: "not json at all"}

	cfg := &config.PlannerConfig{Model: "test-model", SystemPrompt: "Plan."}
	p := planner.New(mock, cfg)
	result, err := p.Plan(context.Background(), "do something")

	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.IsOk() {
		t.Fatal("expected failure result for invalid JSON")
	}
}

func TestPlan_IncompleteSpec(t *testing.T) {
	spec := map[string]any{"goal": "Missing steps and acceptance"}
	specJSON, _ := json.Marshal(spec)
	mock := &mockCLIRunner{response: string(specJSON)}

	cfg := &config.PlannerConfig{Model: "test-model", SystemPrompt: "Plan."}
	p := planner.New(mock, cfg)
	result, err := p.Plan(context.Background(), "incomplete")

	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.IsOk() {
		t.Fatal("expected failure for incomplete spec")
	}
}

func TestPlan_MarkdownFencedJSON(t *testing.T) {
	spec := map[string]any{
		"goal":       "Deploy app",
		"steps":      []string{"Build image", "Push to registry"},
		"acceptance": []string{"Container runs"},
	}
	specJSON, _ := json.Marshal(spec)
	fenced := "```json\n" + string(specJSON) + "\n```"
	mock := &mockCLIRunner{response: fenced}

	cfg := &config.PlannerConfig{Model: "test-model", SystemPrompt: "Plan."}
	p := planner.New(mock, cfg)
	result, err := p.Plan(context.Background(), "deploy")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsOk() {
		t.Fatalf("expected ok for fenced JSON, got: %v", result.Err)
	}
	if result.Value.Goal != "Deploy app" {
		t.Errorf("goal = %q, want %q", result.Value.Goal, "Deploy app")
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

	cfg := &config.PlannerConfig{Model: "test-model", SystemPrompt: "Plan."}
	p := planner.New(&mockCLIRunner{}, cfg)
	parsed, err := p.ParseSpec(fenced)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Goal != "Test fences" {
		t.Errorf("goal = %q, want %q", parsed.Goal, "Test fences")
	}
}
