package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// PlanValidator independently judges whether a specification is complete,
// executable, non-contradictory, and testable.
type PlanValidator struct {
	runner harness.CLIRunner
	cfg    *config.ValidatorConfig
}

// NewPlanValidator creates a plan validator using the given CLIRunner.
func NewPlanValidator(runner harness.CLIRunner, cfg *config.ValidatorConfig) *PlanValidator {
	return &PlanValidator{runner: runner, cfg: cfg}
}

// ValidatePlan runs deterministic checks and then a CLI-based validation.
func (v *PlanValidator) ValidatePlan(ctx context.Context, spec Specification) (*ValidationReport, error) {
	// Phase 1: Deterministic pre-checks
	issues := v.deterministicChecks(spec)
	for _, issue := range issues {
		if issue.Blocking {
			report := &ValidationReport{
				SchemaVersion: "1",
				Verdict:       VerdictFail,
				Summary:       "Specification failed deterministic checks",
				Issues:        issues,
			}
			return report, nil
		}
	}

	// Phase 2: CLI-based validation
	specJSON, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal spec for validation: %w", err)
	}

	prompt := "Validate this specification:\n\n" + string(specJSON)
	result, err := v.runner.RunPrint(ctx, prompt, v.cfg.SystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("validator CLI call: %w", err)
	}

	var report ValidationReport
	if err := json.Unmarshal([]byte(result.Output), &report); err != nil {
		return nil, fmt.Errorf("parse validation report: %w (raw: %s)", err, result.Output)
	}

	// Merge deterministic issues into report
	report.Issues = append(issues, report.Issues...)
	report.Verdict = DeriveVerdict(report.Issues)

	return &report, nil
}

// deterministicChecks performs structural validation that doesn't need an LLM.
func (v *PlanValidator) deterministicChecks(spec Specification) []Issue {
	var issues []Issue

	if spec.Goal == "" {
		issues = append(issues, Issue{
			ID:       "MISSING_GOAL",
			Blocking: true,
			Message:  "Specification has no goal",
		})
	}

	if len(spec.Steps) == 0 {
		issues = append(issues, Issue{
			ID:       "NO_STEPS",
			Blocking: true,
			Message:  "Specification has no steps",
		})
	}

	if len(spec.Acceptance) == 0 {
		issues = append(issues, Issue{
			ID:       "NO_ACCEPTANCE",
			Blocking: true,
			Message:  "Specification has no acceptance criteria",
		})
	}

	// Check for empty steps
	for i, step := range spec.Steps {
		if step == "" {
			issues = append(issues, Issue{
				ID:       fmt.Sprintf("EMPTY_STEP_%d", i),
				Blocking: true,
				Message:  fmt.Sprintf("Step %d is empty", i+1),
			})
		}
	}

	return issues
}
