package types

import (
	"encoding/json"
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
