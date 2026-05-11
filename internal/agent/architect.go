package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// Architect is the senior architect that refines a researcher's draft into a
// final implementation plan. The architect has limited tool access (Read only)
// and performs spot-checks on the researcher's claims.
type Architect struct {
	runner harness.CLIRunner
	cfg    config.ArchitectConfig
}

// NewArchitect creates an Architect backed by the given CLIRunner.
func NewArchitect(runner harness.CLIRunner, cfg config.ArchitectConfig) *Architect {
	return &Architect{runner: runner, cfg: cfg}
}

// Refine takes a researcher's draft markdown and produces the final plan.
func (a *Architect) Refine(ctx context.Context, researcherDraft string) (RawPlan, harness.TokenUsage, string, error) {
	prompt := "Refine this researcher draft into a final implementation plan:\n\n" + researcherDraft
	result, err := a.runner.RunPrint(ctx, prompt, a.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect refine: %w", err)
	}
	return a.extractArchitectPlan(result)
}

// RefineStreaming is like Refine but streams output.
func (a *Architect) RefineStreaming(ctx context.Context, researcherDraft string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := "Refine this researcher draft into a final implementation plan:\n\n" + researcherDraft
	result, err := a.runner.RunStreaming(ctx, prompt, a.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect refine streaming: %w", err)
	}
	return a.extractArchitectPlan(result)
}

// RefineWithComments takes a previous plan and human comments, producing a revised plan.
func (a *Architect) RefineWithComments(ctx context.Context, previousPlan, comments string) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
	result, err := a.runner.RunPrint(ctx, prompt, a.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect refine with comments: %w", err)
	}
	return a.extractArchitectPlan(result)
}

// RefineWithCommentsStreaming is like RefineWithComments but streams output.
func (a *Architect) RefineWithCommentsStreaming(ctx context.Context, previousPlan, comments string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
	result, err := a.runner.RunStreaming(ctx, prompt, a.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect refine with comments streaming: %w", err)
	}
	return a.extractArchitectPlan(result)
}

// continuePromptTemplate is the prompt used when resuming an architect session.
// It includes the current plan as ground truth and the reviewer's message.
// The architect communicates plan changes via the plan file (permission_mode: plan),
// not via stdout formatting.
const continuePromptTemplate = `The current implementation plan is below. The reviewer sent a message.

<current_plan>
%s
</current_plan>

<reviewer_message>
%s
</reviewer_message>

If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
If the reviewer requests changes, revise the plan.`

// ContinueSession resumes the architect's session to handle a reviewer comment.
// It uses RunContinue (--resume) to maintain the full conversation context.
// After the run, it reads the plan file from ~/.claude/plans/ to detect revisions.
// Returns chat response text, the revised plan (nil if unchanged), token usage, and error.
func (a *Architect) ContinueSession(ctx context.Context, sessionID, currentPlan, comment string, stdout io.Writer) (string, *RawPlan, harness.TokenUsage, error) {
	cr, ok := a.runner.(harness.ContinuableRunner)
	if !ok {
		return "", nil, harness.TokenUsage{}, fmt.Errorf("architect runner does not support session continuation")
	}

	prompt := fmt.Sprintf(continuePromptTemplate, currentPlan, comment)
	result, err := cr.RunContinue(ctx, sessionID, prompt, stdout)
	if err != nil {
		return "", nil, harness.TokenUsage{}, fmt.Errorf("architect continue session: %w", err)
	}

	// Read the plan file to detect whether the architect revised it.
	planContent, readErr := ReadPlanFromRun(result)
	if readErr != nil {
		slog.Debug("could not read plan file after continue", "session_id", sessionID, "err", readErr)
		return result.Output, nil, result.Usage, nil
	}

	if planContent != strings.TrimSpace(currentPlan) {
		plan := RawPlan{Markdown: planContent}
		return result.Output, &plan, result.Usage, nil
	}

	return result.Output, nil, result.Usage, nil
}

// extractArchitectPlan reads the plan from the Claude CLI plan file and validates
// that it contains the required ## Work Packages section.
func (a *Architect) extractArchitectPlan(result harness.RunResult) (RawPlan, harness.TokenUsage, string, error) {
	content, err := ReadPlanFromRun(result)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("extract architect plan: %w", err)
	}
	if !strings.Contains(content, "## Work Packages") {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect plan file missing '## Work Packages' section (got: %s)", truncateRaw(content, 100))
	}
	return RawPlan{Markdown: content}, result.Usage, result.SessionID, nil
}
