package agent

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
	"github.com/xiii/orqestra/internal/validator"
)

// mockCLIRunner is a test double for the CLIRunner interface.
type mockCLIRunner struct {
	response string
	err      error
}

func (m *mockCLIRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func (m *mockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func TestBuildExecutionPrompt(t *testing.T) {
	spec := types.Specification{
		Goal:       "Build an API",
		Steps:      []string{"Create server", "Add routes"},
		Acceptance: []string{"Server starts"},
	}
	prompt := buildExecutionPrompt(spec)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !contains(prompt, "Build an API") {
		t.Error("prompt should contain the goal")
	}
	if !contains(prompt, "1. Create server") {
		t.Error("prompt should contain numbered steps")
	}
}

func TestFormatValidationFeedback(t *testing.T) {
	report := &types.ValidationReport{
		Verdict: types.VerdictFail,
		Summary: "Plan is incomplete",
		Issues: []types.Issue{
			{ID: "MISSING", Severity: types.SeverityError, Message: "no tests"},
		},
		Suggestions: []string{"Add test steps"},
	}
	feedback := formatValidationFeedback(report)
	if !contains(feedback, "MISSING") {
		t.Error("feedback should contain issue ID")
	}
	if !contains(feedback, "Add test steps") {
		t.Error("feedback should contain suggestions")
	}
}

func TestAgent_PlanValidation_WithMockRunner(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Retry.PlannerAttempts = 1

	reportJSON, _ := json.Marshal(types.ValidationReport{
		SchemaVersion: "1",
		Verdict:       types.VerdictPass,
		Summary:       "ok",
	})
	mock := &mockCLIRunner{response: string(reportJSON)}
	vcfg := &config.ValidatorConfig{ModelRef: "test"}
	pv := validator.NewPlanValidator(mock, vcfg)

	spec := types.Specification{
		Goal:       "Do thing",
		Steps:      []string{"Step 1"},
		Acceptance: []string{"Done"},
	}

	report, err := pv.Validate(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != types.VerdictPass {
		t.Errorf("expected pass, got %q", report.Verdict)
	}
}

func TestDeriveVerdict(t *testing.T) {
	tests := []struct {
		name   string
		issues []types.Issue
		want   types.Verdict
	}{
		{"no issues", nil, types.VerdictPass},
		{"info only", []types.Issue{{Severity: types.SeverityInfo}}, types.VerdictPass},
		{"warning", []types.Issue{{Severity: types.SeverityWarning}}, types.VerdictWarn},
		{"error", []types.Issue{{Severity: types.SeverityError}}, types.VerdictFail},
		{"mixed", []types.Issue{{Severity: types.SeverityWarning}, {Severity: types.SeverityError}}, types.VerdictFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := types.DeriveVerdict(tt.issues)
			if got != tt.want {
				t.Errorf("DeriveVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Suppress unused import warnings
var (
	_ io.Writer
	_ harness.CLIRunner = (*mockCLIRunner)(nil)
)
