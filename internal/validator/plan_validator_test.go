package validator

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/types"
)

// mockCLIRunner is a test double for harness.CLIRunner.
type mockCLIRunner struct {
	response  string
	err       error
	callCount int
}

func (m *mockCLIRunner) RunPrint(_ context.Context, _, _ string) (string, error) {
	m.callCount++
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func (m *mockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (string, error) {
	m.callCount++
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func TestPlanValidator_DeterministicCheck_MissingGoal(t *testing.T) {
	mock := &mockCLIRunner{response: `{"schema_version":"1","verdict":"pass","summary":"ok"}`}
	cfg := &config.ValidatorConfig{Model: "test"}
	v := NewPlanValidator(mock, cfg)

	report, err := v.Validate(context.Background(), types.Specification{
		Steps:      []string{"do something"},
		Acceptance: []string{"done"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != types.VerdictFail {
		t.Errorf("expected fail verdict for missing goal, got %q", report.Verdict)
	}
	// Should not have called CLI
	if mock.callCount != 0 {
		t.Error("CLI should not be called when deterministic checks fail")
	}
}

func TestPlanValidator_DeterministicCheck_NoSteps(t *testing.T) {
	mock := &mockCLIRunner{response: `{}`}
	cfg := &config.ValidatorConfig{Model: "test"}
	v := NewPlanValidator(mock, cfg)

	report, err := v.Validate(context.Background(), types.Specification{
		Goal:       "something",
		Acceptance: []string{"done"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != types.VerdictFail {
		t.Errorf("expected fail, got %q", report.Verdict)
	}
}

func TestPlanValidator_CLIValidation_Pass(t *testing.T) {
	reportJSON, _ := json.Marshal(types.ValidationReport{
		SchemaVersion: "1",
		Verdict:       types.VerdictPass,
		Summary:       "Plan is clear and executable",
	})
	mock := &mockCLIRunner{response: string(reportJSON)}
	cfg := &config.ValidatorConfig{
		Model:        "test-model",
		SystemPrompt: "validate",
	}
	v := NewPlanValidator(mock, cfg)

	spec := types.Specification{
		Goal:       "Build a REST API",
		Steps:      []string{"Create server", "Add routes", "Test"},
		Acceptance: []string{"Server responds to healthcheck"},
	}

	report, err := v.Validate(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != types.VerdictPass {
		t.Errorf("expected pass, got %q", report.Verdict)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 CLI call, got %d", mock.callCount)
	}
}

func TestPlanValidator_CLIValidation_Fail(t *testing.T) {
	reportJSON, _ := json.Marshal(types.ValidationReport{
		SchemaVersion: "1",
		Verdict:       types.VerdictFail,
		Summary:       "Steps are contradictory",
		Issues: []types.Issue{
			{ID: "CONTRADICTORY", Severity: types.SeverityError, Message: "Step 2 contradicts step 1"},
		},
	})
	mock := &mockCLIRunner{response: string(reportJSON)}
	cfg := &config.ValidatorConfig{Model: "test"}
	v := NewPlanValidator(mock, cfg)

	spec := types.Specification{
		Goal:       "Do stuff",
		Steps:      []string{"Create file", "Delete same file"},
		Acceptance: []string{"File exists"},
	}

	report, err := v.Validate(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != types.VerdictFail {
		t.Errorf("expected fail, got %q", report.Verdict)
	}
}
