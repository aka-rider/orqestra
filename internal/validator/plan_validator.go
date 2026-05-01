package validator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/llm"
	"github.com/xiii/orqestra/internal/types"
)

// PlanValidator independently judges whether a specification is complete,
// executable, non-contradictory, and testable.
type PlanValidator struct {
	provider llm.Provider
	cfg      *config.ValidatorConfig
}

// NewPlanValidator creates a plan validator using the given LLM provider.
func NewPlanValidator(provider llm.Provider, cfg *config.ValidatorConfig) *PlanValidator {
	return &PlanValidator{provider: provider, cfg: cfg}
}

// Validate runs deterministic checks and then an LLM-based validation.
func (v *PlanValidator) Validate(ctx context.Context, spec types.Specification) (*types.ValidationReport, error) {
	// Phase 1: Deterministic pre-checks
	issues := v.deterministicChecks(spec)
	for _, issue := range issues {
		if issue.Severity == types.SeverityError {
			report := &types.ValidationReport{
				SchemaVersion: "1",
				Verdict:       types.VerdictFail,
				Summary:       "Specification failed deterministic checks",
				Issues:        issues,
			}
			return report, nil
		}
	}

	// Phase 2: LLM-based validation
	specJSON, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal spec for validation: %w", err)
	}

	resp, err := v.provider.Generate(ctx, &llm.Request{
		Model:        v.cfg.Model,
		SystemPrompt: v.cfg.SystemPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: "Validate this specification:\n\n" + string(specJSON)},
		},
		ResponseJSON: true,
	})
	if err != nil {
		return nil, fmt.Errorf("validator LLM call: %w", err)
	}

	var report types.ValidationReport
	if err := json.Unmarshal([]byte(resp.Content), &report); err != nil {
		return nil, fmt.Errorf("parse validation report: %w (raw: %s)", err, resp.Content)
	}

	// Merge deterministic issues into report
	report.Issues = append(issues, report.Issues...)
	report.Verdict = types.DeriveVerdict(report.Issues)

	return &report, nil
}

// deterministicChecks performs structural validation that doesn't need an LLM.
func (v *PlanValidator) deterministicChecks(spec types.Specification) []types.Issue {
	var issues []types.Issue

	if spec.Goal == "" {
		issues = append(issues, types.Issue{
			ID:       "MISSING_GOAL",
			Severity: types.SeverityError,
			Message:  "Specification has no goal",
		})
	}

	if len(spec.Steps) == 0 {
		issues = append(issues, types.Issue{
			ID:       "NO_STEPS",
			Severity: types.SeverityError,
			Message:  "Specification has no steps",
		})
	}

	if len(spec.Acceptance) == 0 {
		issues = append(issues, types.Issue{
			ID:       "NO_ACCEPTANCE",
			Severity: types.SeverityError,
			Message:  "Specification has no acceptance criteria",
		})
	}

	// Check for empty steps
	for i, step := range spec.Steps {
		if step == "" {
			issues = append(issues, types.Issue{
				ID:       fmt.Sprintf("EMPTY_STEP_%d", i),
				Severity: types.SeverityError,
				Message:  fmt.Sprintf("Step %d is empty", i+1),
			})
		}
	}

	return issues
}
