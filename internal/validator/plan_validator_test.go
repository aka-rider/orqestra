package validator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/llm"
	"github.com/xiii/orqestra/internal/types"
)

func TestPlanValidator_DeterministicCheck_MissingGoal(t *testing.T) {
	mock := &llm.MockProvider{IDValue: "test", Response: `{"schema_version":"1","verdict":"pass","summary":"ok"}`}
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
	// Should not have called LLM
	if mock.CallCount != 0 {
		t.Error("LLM should not be called when deterministic checks fail")
	}
}

func TestPlanValidator_DeterministicCheck_NoSteps(t *testing.T) {
	mock := &llm.MockProvider{IDValue: "test", Response: `{}`}
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

func TestPlanValidator_LLMValidation_Pass(t *testing.T) {
	reportJSON, _ := json.Marshal(types.ValidationReport{
		SchemaVersion: "1",
		Verdict:       types.VerdictPass,
		Summary:       "Plan is clear and executable",
	})
	mock := &llm.MockProvider{IDValue: "test", Response: string(reportJSON)}
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
	if mock.CallCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", mock.CallCount)
	}
}

func TestPlanValidator_LLMValidation_Fail(t *testing.T) {
	reportJSON, _ := json.Marshal(types.ValidationReport{
		SchemaVersion: "1",
		Verdict:       types.VerdictFail,
		Summary:       "Steps are contradictory",
		Issues: []types.Issue{
			{ID: "CONTRADICTORY", Severity: types.SeverityError, Message: "Step 2 contradicts step 1"},
		},
	})
	mock := &llm.MockProvider{IDValue: "test", Response: string(reportJSON)}
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
