package types

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
		Constraints:       []string{"No external deps"},
		Assumptions:       []string{"Go installed"},
		Risks:             []string{"Might be slow"},
		AllowedOperations: []string{"read", "write"},
		ExpectedArtifacts: []string{"main.go"},
		ValidationCommands: []ValidationCommand{
			{Command: "go", Args: []string{"test", "./..."}, ExpectedExit: 0},
		},
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
	if len(decoded.ValidationCommands) != 1 {
		t.Errorf("validation commands: got %d, want 1", len(decoded.ValidationCommands))
	}
}

func TestDeriveVerdict(t *testing.T) {
	tests := []struct {
		name   string
		issues []Issue
		want   Verdict
	}{
		{"no issues", nil, VerdictPass},
		{"info only", []Issue{{Severity: SeverityInfo}}, VerdictPass},
		{"warning only", []Issue{{Severity: SeverityWarning}}, VerdictWarn},
		{"error", []Issue{{Severity: SeverityError}}, VerdictFail},
		{"error overrides warning", []Issue{{Severity: SeverityWarning}, {Severity: SeverityError}}, VerdictFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveVerdict(tt.issues)
			if got != tt.want {
				t.Errorf("DeriveVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidationReport_JSONRoundtrip(t *testing.T) {
	report := ValidationReport{
		SchemaVersion: "1",
		Verdict:       VerdictWarn,
		Summary:       "Minor issues",
		Issues: []Issue{
			{ID: "WARN_1", Severity: SeverityWarning, Message: "Could be better"},
		},
		Suggestions: []string{"Add more tests"},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ValidationReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Verdict != report.Verdict {
		t.Errorf("verdict: got %q, want %q", decoded.Verdict, report.Verdict)
	}
	if len(decoded.Issues) != 1 {
		t.Errorf("issues: got %d, want 1", len(decoded.Issues))
	}
}

func TestProjectPlan_JSONRoundtrip(t *testing.T) {
	plan := ProjectPlan{
		SchemaVersion: "1",
		Packages: []WorkPackage{
			{
				ID:          "api-routes",
				Title:       "Implement API routes",
				Steps:       []string{"Create handler", "Add middleware"},
				Acceptance:  []string{"GET /health returns 200"},
				Constraints: []string{"No breaking changes"},
			},
			{
				ID:         "frontend",
				Title:      "Build frontend",
				Steps:      []string{"Create React app"},
				Acceptance: []string{"npm test passes"},
				DependsOn:  []string{"api-routes"},
			},
		},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ProjectPlan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Packages) != 2 {
		t.Fatalf("packages: got %d, want 2", len(decoded.Packages))
	}
	if decoded.Packages[0].ID != "api-routes" {
		t.Errorf("pkg[0].ID = %q, want %q", decoded.Packages[0].ID, "api-routes")
	}
	if len(decoded.Packages[1].DependsOn) != 1 || decoded.Packages[1].DependsOn[0] != "api-routes" {
		t.Errorf("pkg[1].DependsOn = %v, want [api-routes]", decoded.Packages[1].DependsOn)
	}
}

func TestWorkPackage_ToSpecification(t *testing.T) {
	parent := Specification{
		SchemaVersion: "1",
		Goal:          "Build full-stack app",
		Context:       "Go + React monorepo",
		Scope:         &Scope{IncludeGlobs: []string{"src/**"}},
	}
	wp := WorkPackage{
		ID:          "backend",
		Title:       "Implement backend API",
		Steps:       []string{"Create server", "Add routes"},
		Acceptance:  []string{"go test passes"},
		Constraints: []string{"No external deps"},
	}

	spec := wp.ToSpecification(parent)

	if spec.Goal != "Implement backend API" {
		t.Errorf("Goal = %q, want %q", spec.Goal, "Implement backend API")
	}
	if spec.Context != parent.Context {
		t.Errorf("Context not inherited from parent")
	}
	if spec.Scope == nil {
		t.Error("Scope should be inherited from parent")
	}
	if len(spec.Steps) != 2 {
		t.Errorf("Steps: got %d, want 2", len(spec.Steps))
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

func TestFormatValidationFeedback(t *testing.T) {
	report := &ValidationReport{
		Verdict: VerdictFail,
		Summary: "Plan is incomplete",
		Issues: []Issue{
			{ID: "MISSING", Severity: SeverityError, Message: "no tests"},
		},
		Suggestions: []string{"Add test steps"},
	}
	feedback := FormatValidationFeedback(report)
	if !strings.Contains(feedback, "MISSING") {
		t.Error("feedback should contain issue ID")
	}
	if !strings.Contains(feedback, "Add test steps") {
		t.Error("feedback should contain suggestions")
	}
}

func TestTopoWaves(t *testing.T) {
	packages := []WorkPackage{
		{ID: "a", Steps: []string{"do a"}},
		{ID: "b", Steps: []string{"do b"}, DependsOn: []string{"a"}},
		{ID: "c", Steps: []string{"do c"}},
		{ID: "d", Steps: []string{"do d"}, DependsOn: []string{"b", "c"}},
	}
	waves := TopoWaves(packages)

	if len(waves) < 2 {
		t.Fatalf("expected at least 2 waves, got %d", len(waves))
	}

	// Wave 0 should contain a and c (no deps)
	wave0IDs := map[string]bool{}
	for _, wp := range waves[0] {
		wave0IDs[wp.ID] = true
	}
	if !wave0IDs["a"] || !wave0IDs["c"] {
		t.Errorf("wave 0 should contain a and c, got %v", waves[0])
	}

	// d should be in a later wave than both b and c
	dWave := -1
	bWave := -1
	for i, wave := range waves {
		for _, wp := range wave {
			if wp.ID == "d" {
				dWave = i
			}
			if wp.ID == "b" {
				bWave = i
			}
		}
	}
	if dWave <= bWave {
		t.Errorf("d (wave %d) should come after b (wave %d)", dWave, bWave)
	}
}
