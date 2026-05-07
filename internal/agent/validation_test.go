package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

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
