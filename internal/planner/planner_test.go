package planner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/planner"
)

// newTestClient creates a Client backed by a mock HTTP server that returns
// the given mockOutput as the assistant message content.
func newTestClient(t *testing.T, mockOutput string) (*harness.Client, func()) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": mockOutput}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))

	client := harness.NewClient("test-model", nil)
	client.BaseURL = srv.URL
	return client, srv.Close
}

func TestPlan_Success(t *testing.T) {
	spec := map[string]any{
		"goal":       "Build a REST API",
		"steps":      []string{"Create main.go", "Add handler", "Write tests"},
		"acceptance": []string{"Server starts", "GET /health returns 200"},
	}
	specJSON, _ := json.Marshal(spec)
	client, cleanup := newTestClient(t, string(specJSON))
	defer cleanup()

	cfg := &config.PlannerConfig{
		Model:        "test-model",
		SystemPrompt: "Plan mode.",
	}

	p := planner.New(client, cfg)
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
	client, cleanup := newTestClient(t, envelope)
	defer cleanup()

	cfg := &config.PlannerConfig{Model: "test-model", SystemPrompt: "Plan."}
	p := planner.New(client, cfg)
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
	client, cleanup := newTestClient(t, "not json at all")
	defer cleanup()

	cfg := &config.PlannerConfig{Model: "test-model", SystemPrompt: "Plan."}
	p := planner.New(client, cfg)
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
	client, cleanup := newTestClient(t, string(specJSON))
	defer cleanup()

	cfg := &config.PlannerConfig{Model: "test-model", SystemPrompt: "Plan."}
	p := planner.New(client, cfg)
	result, err := p.Plan(context.Background(), "incomplete")

	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.IsOk() {
		t.Fatal("expected failure for incomplete spec")
	}
}
