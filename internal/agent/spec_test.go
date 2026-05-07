package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSpecification_JSONRoundtrip(t *testing.T) {
	spec := Specification{
		SchemaVersion: "1",
		ID:            "test-001",
		Title:         "Test Spec",
		Goal:          "Build a thing",
		Context:       "Some context",
		Steps:         []string{"Step 1", "Step 2"},
		Acceptance:    []string{"It works"},
		Scope: &Scope{
			IncludeGlobs: []string{"src/**"},
			ExcludeGlobs: []string{"vendor/**"},
		},
		Constraints: []string{"No external deps"},
		Assumptions: []string{"Go installed"},
		Risks:       []string{"Might be slow"},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Specification
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Goal != spec.Goal {
		t.Errorf("goal: got %q, want %q", decoded.Goal, spec.Goal)
	}
	if len(decoded.Steps) != len(spec.Steps) {
		t.Errorf("steps: got %d, want %d", len(decoded.Steps), len(spec.Steps))
	}
	if decoded.Scope == nil {
		t.Fatal("scope should not be nil")
	}
}

func TestBuildExecutionPrompt(t *testing.T) {
	spec := Specification{
		Goal:       "Build an API",
		Steps:      []string{"Create server", "Add routes"},
		Acceptance: []string{"Server starts"},
	}
	prompt := BuildExecutionPrompt(spec)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "Build an API") {
		t.Error("prompt should contain the goal")
	}
	if !strings.Contains(prompt, "1. Create server") {
		t.Error("prompt should contain numbered steps")
	}
	if !strings.Contains(prompt, "Server starts") {
		t.Error("prompt should contain acceptance criteria")
	}
}
