package validator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
)

// WorkValidationInput contains everything needed to validate work output.
type WorkValidationInput struct {
	Spec       types.Specification
	WorkOutput string
}

// defaultAllowedCommands is the allowlist of commands permitted for validation execution.
// Only these commands may be invoked from LLM-generated ValidationCommands.
var defaultAllowedCommands = map[string]bool{
	"go":     true,
	"make":   true,
	"npm":    true,
	"npx":    true,
	"yarn":   true,
	"pnpm":   true,
	"cargo":  true,
	"python": true,
	"pytest": true,
	"true":   true,
	"false":  true,
	"test":   true,
	"diff":   true,
	"grep":   true,
	"cat":    true,
	"ls":     true,
	"find":   true,
	"wc":     true,
	"head":   true,
	"tail":   true,
}

// WorkValidator independently validates the work output against the specification.
type WorkValidator struct {
	runner          harness.CLIRunner
	cfg             *config.ValidatorConfig
	allowedCommands map[string]bool
}

// NewWorkValidator creates a work validator using the given CLIRunner.
func NewWorkValidator(runner harness.CLIRunner, cfg *config.ValidatorConfig) *WorkValidator {
	return &WorkValidator{
		runner:          runner,
		cfg:             cfg,
		allowedCommands: defaultAllowedCommands,
	}
}

// Validate runs validation commands and then CLI-based assessment.
func (v *WorkValidator) Validate(ctx context.Context, input *WorkValidationInput) (*types.ValidationReport, error) {
	var issues []types.Issue
	var cmdResults []types.ValidationCommandResult

	// Phase 1: Run validation commands
	for i, vc := range input.Spec.ValidationCommands {
		result := v.runValidationCommand(ctx, vc)
		cmdResults = append(cmdResults, result)
		if !result.Passed {
			issues = append(issues, types.Issue{
				ID:       fmt.Sprintf("CMD_FAIL_%d", i),
				Severity: types.SeverityError,
				Message:  fmt.Sprintf("Validation command %q exited %d (expected %d)", vc.Command, result.ActualExit, vc.ExpectedExit),
			})
		}
	}

	// If deterministic command checks failed, return early
	if types.DeriveVerdict(issues) == types.VerdictFail {
		return &types.ValidationReport{
			SchemaVersion: "1",
			Verdict:       types.VerdictFail,
			Summary:       "Validation commands failed",
			Issues:        issues,
		}, nil
	}

	// Phase 2: CLI-based validation
	specJSON, err := json.MarshalIndent(input.Spec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal spec: %w", err)
	}

	prompt := fmt.Sprintf("Original Specification:\n%s\n\nExecution Output:\n%s",
		string(specJSON), truncate(input.WorkOutput, 8000))

	if len(cmdResults) > 0 {
		cmdJSON, _ := json.MarshalIndent(cmdResults, "", "  ")
		prompt += fmt.Sprintf("\n\nValidation Command Results:\n%s", string(cmdJSON))
	}

	result, err := v.runner.RunPrint(ctx, prompt, v.cfg.SystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("work validator CLI call: %w", err)
	}

	var report types.ValidationReport
	if err := json.Unmarshal([]byte(result.Output), &report); err != nil {
		return nil, fmt.Errorf("parse work validation report: %w (raw: %s)", err, result.Output)
	}

	// Merge command-failure issues
	report.Issues = append(issues, report.Issues...)
	report.Verdict = types.DeriveVerdict(report.Issues)

	return &report, nil
}

// runValidationCommand executes a single validation command and captures the result.
// It enforces the command allowlist to prevent execution of arbitrary commands from LLM output.
func (v *WorkValidator) runValidationCommand(ctx context.Context, vc types.ValidationCommand) types.ValidationCommandResult {
	result := types.ValidationCommandResult{
		Command:      vc.Command,
		Args:         vc.Args,
		Cwd:          vc.Cwd,
		ExpectedExit: vc.ExpectedExit,
	}

	// Security gate: reject commands not in the allowlist
	if !v.allowedCommands[vc.Command] {
		slog.Warn("validation command blocked by allowlist", "command", vc.Command)
		result.ActualExit = -1
		result.Stderr = fmt.Sprintf("command %q blocked: not in validation allowlist", vc.Command)
		result.Passed = false
		return result
	}

	args := vc.Args
	cmd := exec.CommandContext(ctx, vc.Command, args...)
	if vc.Cwd != "" {
		cmd.Dir = vc.Cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result.Stdout = truncate(stdout.String(), 2000)
	result.Stderr = truncate(stderr.String(), 2000)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ActualExit = exitErr.ExitCode()
		} else {
			result.ActualExit = -1
		}
	}

	result.Passed = result.ActualExit == vc.ExpectedExit
	return result
}

// truncate limits a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n...(truncated)"
}
