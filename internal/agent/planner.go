package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// Planner is the senior architect that refines a researcher's draft into a
// final implementation plan. The planner has limited tool access (Read only)
// and performs spot-checks on the researcher's claims.
type Planner struct {
	runner harness.CLIRunner
	cfg    config.PlannerConfig
}

// NewPlanner creates a Planner backed by the given CLIRunner.
func NewPlanner(runner harness.CLIRunner, cfg config.PlannerConfig) *Planner {
	return &Planner{runner: runner, cfg: cfg}
}

// Refine takes a researcher's draft markdown and produces the final plan.
func (p *Planner) Refine(ctx context.Context, researcherDraft string) (RawPlan, harness.TokenUsage, string, error) {
	prompt := "Refine this researcher draft into a final implementation plan:\n\n" + researcherDraft
	result, err := p.runner.RunPrint(ctx, prompt, p.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner refine: %w", err)
	}
	return p.parsePlanResult(result)
}

// RefineStreaming is like Refine but streams output.
func (p *Planner) RefineStreaming(ctx context.Context, researcherDraft string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := "Refine this researcher draft into a final implementation plan:\n\n" + researcherDraft
	result, err := p.runner.RunStreaming(ctx, prompt, p.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner refine streaming: %w", err)
	}
	return p.parsePlanResult(result)
}

// RefineWithComments takes a previous plan and human comments, producing a revised plan.
func (p *Planner) RefineWithComments(ctx context.Context, previousPlan, comments string) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
	result, err := p.runner.RunPrint(ctx, prompt, p.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner refine with comments: %w", err)
	}
	return p.parsePlanResult(result)
}

// RefineWithCommentsStreaming is like RefineWithComments but streams output.
func (p *Planner) RefineWithCommentsStreaming(ctx context.Context, previousPlan, comments string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
	result, err := p.runner.RunStreaming(ctx, prompt, p.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner refine with comments streaming: %w", err)
	}
	return p.parsePlanResult(result)
}

// parsePlanResult extracts markdown from a run result and performs basic sanity checks.
func (p *Planner) parsePlanResult(result harness.RunResult) (RawPlan, harness.TokenUsage, string, error) {
	md := strings.TrimSpace(stripCodeFences(result.Output))

	// Basic sanity: must start with "# Plan" and contain "## Work Packages"
	if !strings.HasPrefix(md, "# Plan") {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner output does not start with '# Plan' (got: %s)", truncateRaw(md, 100))
	}
	if !strings.Contains(md, "## Work Packages") {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner output missing '## Work Packages' section")
	}

	return RawPlan{
		Markdown: md,
	}, result.Usage, result.SessionID, nil
}

// truncateRaw limits a raw string for error messages.
func truncateRaw(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
