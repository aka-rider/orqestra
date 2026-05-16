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
func (a *Architect) Refine(ctx context.Context, userPrompt string, researcherFacts string) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("<user_request>\n%s\n</user_request>\n\n<codebase_research>\n%s\n</codebase_research>\n\nUsing the codebase research above, produce an implementation plan for the user's request.",
		userPrompt, researcherFacts)
	result, err := a.runner.RunPrint(ctx, prompt, a.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect refine: %w", err)
	}
	return a.extractArchitectPlan(result)
}

// RefineStreaming is like Refine but streams output.
func (a *Architect) RefineStreaming(ctx context.Context, userPrompt string, researcherFacts string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("<user_request>\n%s\n</user_request>\n\n<codebase_research>\n%s\n</codebase_research>\n\nUsing the codebase research above, produce an implementation plan for the user's request.",
		userPrompt, researcherFacts)
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

// continueWithDiffTemplate is used when the reviewer edited the plan directly
// (Ctrl+E) and optionally added a comment. The diff shows exactly what changed.
const continueWithDiffTemplate = `The reviewer edited the plan directly. Here are their changes:

<plan_changes>
%s
</plan_changes>

<current_plan>
%s
</current_plan>

<reviewer_message>
%s
</reviewer_message>

Focus on the reviewer's edits first. If the reviewer asks a question, answer it.
If the reviewer requests further changes, revise the plan.`

// ContinueSession resumes the architect's session to handle a reviewer comment.
// It uses RunContinue (--resume) to maintain the full conversation context.
// Revision detection requires two conditions:
//  1. The plan file changed from its pre-run baseline (the architect wrote something).
//  2. The new content differs from currentPlan (it's not just echoing user edits).
//
// Returns chat response text, the revised plan (nil if unchanged), token usage, and error.
func (a *Architect) ContinueSession(ctx context.Context, sessionID, currentPlan, comment string, stdout io.Writer) (string, *RawPlan, harness.TokenUsage, error) {
	cr, ok := a.runner.(harness.ContinuableRunner)
	if !ok {
		return "", nil, harness.TokenUsage{}, fmt.Errorf("architect runner does not support session continuation")
	}

	// Snapshot plan file before continuation to detect real changes.
	baseline, baselineErr := ReadPlanFromRun(harness.RunResult{SessionID: sessionID})
	if baselineErr != nil {
		slog.Debug("could not snapshot plan file baseline, will compare against currentPlan",
			"session_id", sessionID, "err", baselineErr)
	}

	prompt := fmt.Sprintf(continuePromptTemplate, currentPlan, comment)
	result, err := cr.RunContinue(ctx, sessionID, prompt, stdout)
	if err != nil {
		return "", nil, harness.TokenUsage{}, fmt.Errorf("architect continue session: %w", err)
	}

	plan := detectPlanRevision(result, baseline, baselineErr, currentPlan)
	return result.Output, plan, result.Usage, nil
}

// ContinueSessionWithDiff resumes the architect's session with a plain diff
// showing what the reviewer changed, the full current plan, and an optional comment.
// Used when the reviewer edits via Ctrl+E and provides context.
func (a *Architect) ContinueSessionWithDiff(ctx context.Context, sessionID, currentPlan, diff, comment string, stdout io.Writer) (string, *RawPlan, harness.TokenUsage, error) {
	cr, ok := a.runner.(harness.ContinuableRunner)
	if !ok {
		return "", nil, harness.TokenUsage{}, fmt.Errorf("architect runner does not support session continuation")
	}

	baseline, baselineErr := ReadPlanFromRun(harness.RunResult{SessionID: sessionID})
	if baselineErr != nil {
		slog.Debug("could not snapshot plan file baseline for diff review",
			"session_id", sessionID, "err", baselineErr)
	}

	prompt := fmt.Sprintf(continueWithDiffTemplate, diff, currentPlan, comment)
	result, err := cr.RunContinue(ctx, sessionID, prompt, stdout)
	if err != nil {
		return "", nil, harness.TokenUsage{}, fmt.Errorf("architect continue with diff: %w", err)
	}

	plan := detectPlanRevision(result, baseline, baselineErr, currentPlan)
	return result.Output, plan, result.Usage, nil
}

// extractArchitectPlan reads the plan from the Claude CLI plan file and validates
// that it contains content. It also runs a structural health check.
func (a *Architect) extractArchitectPlan(result harness.RunResult) (RawPlan, harness.TokenUsage, string, error) {
	content, err := ReadPlanFromRun(result)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("extract architect plan: %w", err)
	}

	warnings := CheckPlanHealth(content)

	return RawPlan{Markdown: content, Warnings: warnings}, result.Usage, result.SessionID, nil
}

// detectPlanRevision checks whether the architect produced a meaningful plan
// revision during a continuation. A revision is meaningful when both:
//  1. The plan file changed from its pre-run baseline (the architect wrote something).
//  2. The new content differs from currentPlan (it's not just echoing user edits).
//
// Returns nil if the plan file is unreadable, unchanged from baseline, or
// identical to what the user already has (echo suppression).
func detectPlanRevision(result harness.RunResult, baseline string, baselineErr error, currentPlan string) *RawPlan {
	planContent, readErr := ReadPlanFromRun(result)
	if readErr != nil {
		slog.Debug("revision detection: could not read plan file", "err", readErr)
		return nil
	}

	// Condition 1: did the plan file change from its pre-run state?
	compareTo := strings.TrimSpace(currentPlan)
	if baselineErr == nil {
		compareTo = strings.TrimSpace(baseline)
	}
	if planContent == compareTo {
		slog.Debug("revision detection: plan unchanged from baseline",
			"baseline_len", len(compareTo), "plan_len", len(planContent))
		return nil
	}

	// Condition 2: is the new content actually different from what the user has?
	// Without this check, the architect echoing user's ^E edits into its plan
	// file would be presented as a "revision" — the user's own work shown back.
	if planContent == strings.TrimSpace(currentPlan) {
		slog.Debug("revision detection: echo suppressed — matches current plan",
			"plan_len", len(planContent))
		return nil
	}

	slog.Debug("revision detection: plan revised",
		"baseline_len", len(compareTo), "current_len", len(strings.TrimSpace(currentPlan)),
		"new_len", len(planContent))
	warnings := CheckPlanHealth(planContent)
	return &RawPlan{Markdown: planContent, Warnings: warnings}
}

// criticContinueTemplate is the prompt used when resuming the architect session
// with a critic's report. Uses <critic_report> tag to distinguish from human
// reviewer <reviewer_message> feedback.
const criticContinueTemplate = `A Plan Critic agent reviewed your plan and produced the report below.
The Critic had read-only tool access and spot-checked your claims
against the codebase.

<critic_report>
%s
</critic_report>

<current_plan>
%s
</current_plan>

Review every finding. For each:
- If you can verify the issue is real and you know the fix: apply it to
  the plan.
- If you cannot determine whether the issue is valid, or the fix requires
  a judgment call you cannot make: surface it inline in the relevant
  section of the plan, clearly marked with ⚠ CRITIC FLAG, so the human
  reviewer can decide.

Do NOT discard findings silently. Every finding must be either fixed or
flagged.

Output the COMPLETE updated plan starting with "# Plan". Even if you
judge all findings to be non-issues, re-output the plan with inline
notes explaining why.`

// ContinueWithCriticReport resumes the architect's session with a critic report.
// The architect reviews the critic's findings and either fixes issues in the plan
// or surfaces uncertain ones inline with ⚠ CRITIC FLAG markers.
// Uses the same revision detection as ContinueSession (baseline + echo suppression).
// Returns chat response text, the revised plan (nil if unchanged), token usage, and error.
func (a *Architect) ContinueWithCriticReport(ctx context.Context, sessionID, currentPlan, criticReport string, stdout io.Writer) (string, *RawPlan, harness.TokenUsage, error) {
	cr, ok := a.runner.(harness.ContinuableRunner)
	if !ok {
		return "", nil, harness.TokenUsage{}, fmt.Errorf("architect runner does not support session continuation")
	}

	// Snapshot plan file baseline before continuation (see ContinueSession).
	baseline, baselineErr := ReadPlanFromRun(harness.RunResult{SessionID: sessionID})
	if baselineErr != nil {
		slog.Debug("could not snapshot plan file baseline for critic review, will compare against currentPlan",
			"session_id", sessionID, "err", baselineErr)
	}

	prompt := fmt.Sprintf(criticContinueTemplate, criticReport, currentPlan)
	result, err := cr.RunContinue(ctx, sessionID, prompt, stdout)
	if err != nil {
		return "", nil, harness.TokenUsage{}, fmt.Errorf("architect continue with critic report: %w", err)
	}

	plan := detectPlanRevision(result, baseline, baselineErr, currentPlan)
	return result.Output, plan, result.Usage, nil
}
