package validator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/llm"
	"github.com/xiii/orqestra/internal/types"
)

func TestWorkValidator_PassingCommands(t *testing.T) {
	reportJSON, _ := json.Marshal(types.ValidationReport{
		SchemaVersion: "1",
		Verdict:       types.VerdictPass,
		Summary:       "Work output satisfies all criteria",
	})
	mock := &llm.MockProvider{IDValue: "test", Response: string(reportJSON)}
	cfg := &config.WorkValidatorConfig{Model: "test"}
	v := NewWorkValidator(mock, cfg)

	input := &WorkValidationInput{
		Spec: types.Specification{
			Goal:       "Create a file",
			Steps:      []string{"touch file.txt"},
			Acceptance: []string{"file.txt exists"},
			ValidationCommands: []types.ValidationCommand{
				{Command: "true", ExpectedExit: 0},
			},
		},
		WorkOutput: "Created file.txt successfully",
	}

	report, err := v.Validate(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != types.VerdictPass {
		t.Errorf("expected pass, got %q", report.Verdict)
	}
}

func TestWorkValidator_FailingCommand(t *testing.T) {
	mock := &llm.MockProvider{IDValue: "test", Response: `{}`}
	cfg := &config.WorkValidatorConfig{Model: "test"}
	v := NewWorkValidator(mock, cfg)

	input := &WorkValidationInput{
		Spec: types.Specification{
			Goal:       "Create a file",
			Steps:      []string{"touch file.txt"},
			Acceptance: []string{"file.txt exists"},
			ValidationCommands: []types.ValidationCommand{
				{Command: "false", ExpectedExit: 0}, // false always exits 1
			},
		},
		WorkOutput: "something went wrong",
	}

	report, err := v.Validate(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != types.VerdictFail {
		t.Errorf("expected fail, got %q", report.Verdict)
	}
	// Should not call LLM when commands fail
	if mock.CallCount != 0 {
		t.Error("LLM should not be called when validation commands fail")
	}
}

func TestWorkValidator_NoCommands_LLMOnly(t *testing.T) {
	reportJSON, _ := json.Marshal(types.ValidationReport{
		SchemaVersion: "1",
		Verdict:       types.VerdictWarn,
		Summary:       "Looks done but no way to verify automatically",
		Issues: []types.Issue{
			{ID: "AMBIGUOUS", Severity: types.SeverityWarning, Message: "Cannot verify output format"},
		},
	})
	mock := &llm.MockProvider{IDValue: "test", Response: string(reportJSON)}
	cfg := &config.WorkValidatorConfig{Model: "test"}
	v := NewWorkValidator(mock, cfg)

	input := &WorkValidationInput{
		Spec: types.Specification{
			Goal:       "Write docs",
			Steps:      []string{"Write README"},
			Acceptance: []string{"README is clear"},
		},
		WorkOutput: "# My Project\nThis does stuff.",
	}

	report, err := v.Validate(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != types.VerdictWarn {
		t.Errorf("expected warn, got %q", report.Verdict)
	}
	if mock.CallCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", mock.CallCount)
	}
}
