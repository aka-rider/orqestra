package agent

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// validatorMockCLIRunner is a test double for harness.CLIRunner in validator tests.
type validatorMockCLIRunner struct {
	response  string
	err       error
	callCount int
}

func (m *validatorMockCLIRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	m.callCount++
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func (m *validatorMockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	m.callCount++
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func TestPlanValidator_DeterministicCheck_MissingGoal(t *testing.T) {
	mock := &validatorMockCLIRunner{response: `{"schema_version":"1","verdict":"pass","summary":"ok"}`}
	cfg := &config.ValidatorConfig{ModelRef: "test"}
	v := NewPlanValidator(mock, cfg)

	report, err := v.ValidatePlan(context.Background(), Specification{
		Steps:      []string{"do something"},
		Acceptance: []string{"done"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != VerdictFail {
		t.Errorf("expected fail verdict for missing goal, got %q", report.Verdict)
	}
	// Should not have called CLI
	if mock.callCount != 0 {
		t.Error("CLI should not be called when deterministic checks fail")
	}
}

func TestPlanValidator_DeterministicCheck_NoSteps(t *testing.T) {
	mock := &validatorMockCLIRunner{response: `{}`}
	cfg := &config.ValidatorConfig{ModelRef: "test"}
	v := NewPlanValidator(mock, cfg)

	report, err := v.ValidatePlan(context.Background(), Specification{
		Goal:       "something",
		Acceptance: []string{"done"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != VerdictFail {
		t.Errorf("expected fail, got %q", report.Verdict)
	}
}

func TestPlanValidator_CLIValidation_Pass(t *testing.T) {
	reportJSON, _ := json.Marshal(ValidationReport{
		SchemaVersion: "1",
		Verdict:       VerdictPass,
		Summary:       "Plan is clear and executable",
	})
	mock := &validatorMockCLIRunner{response: string(reportJSON)}
	cfg := &config.ValidatorConfig{
		ModelRef:     "test-model",
		SystemPrompt: "validate",
	}
	v := NewPlanValidator(mock, cfg)

	spec := Specification{
		Goal:       "Build a REST API",
		Steps:      []string{"Create server", "Add routes", "Test"},
		Acceptance: []string{"Server responds to healthcheck"},
	}

	report, err := v.ValidatePlan(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != VerdictPass {
		t.Errorf("expected pass, got %q", report.Verdict)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 CLI call, got %d", mock.callCount)
	}
}

func TestPlanValidator_CLIValidation_Fail(t *testing.T) {
	reportJSON, _ := json.Marshal(ValidationReport{
		SchemaVersion: "1",
		Verdict:       VerdictFail,
		Summary:       "Steps are contradictory",
		Issues: []Issue{
			{ID: "CONTRADICTORY", Severity: SeverityError, Message: "Step 2 contradicts step 1"},
		},
	})
	mock := &validatorMockCLIRunner{response: string(reportJSON)}
	cfg := &config.ValidatorConfig{ModelRef: "test"}
	v := NewPlanValidator(mock, cfg)

	spec := Specification{
		Goal:       "Do stuff",
		Steps:      []string{"Create file", "Delete same file"},
		Acceptance: []string{"File exists"},
	}

	report, err := v.ValidatePlan(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != VerdictFail {
		t.Errorf("expected fail, got %q", report.Verdict)
	}
}
