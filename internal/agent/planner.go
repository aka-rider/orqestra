package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/xiii/orqestra/internal/harness"
)

// PlanResult is the output of a Planner.Run or Planner.Continue call.
type PlanResult struct {
	// Plan is the plan file content. Always populated on Run. May be empty on
	// Continue when the model chatted without editing the plan file.
	Plan string
	// Chat is the stream result text. Always populated. Used for gate Q&A responses.
	Chat string
	// Usage is the token consumption for this call.
	Usage harness.TokenUsage
	// SessionID is the Claude session identifier for this call.
	SessionID string
}

// Planner is the unified plan-mode agent entity. It wraps a ContinuableRunner
// and a system prompt, always reading its authoritative output from the plan file.
// Worker execution does not use Planner — workers use ContinuableRunner directly.
type Planner struct {
	runner harness.ContinuableRunner
	system string
}

// NewPlanner creates a Planner backed by the given ContinuableRunner and system prompt.
func NewPlanner(runner harness.ContinuableRunner, system string) *Planner {
	return &Planner{runner: runner, system: system}
}

// Run executes a new planning session with the given prompt. It reads authoritative
// output from the plan file via ReadPlanFromRun. Returns a hard error if the plan
// file is unreadable — initial runs must produce a plan.
func (p *Planner) Run(ctx context.Context, prompt string, stdout io.Writer) (PlanResult, error) {
	result, err := p.runner.RunStreaming(ctx, prompt, p.system, stdout)
	if err != nil {
		return PlanResult{}, fmt.Errorf("planner run: %w", err)
	}

	planContent, planErr := ReadPlanFromRun(result)
	if planErr != nil {
		return PlanResult{}, fmt.Errorf("planner run: read plan file: %w", planErr)
	}

	return PlanResult{
		Plan:      planContent,
		Chat:      result.Output,
		Usage:     result.Usage,
		SessionID: result.SessionID,
	}, nil
}

// Continue resumes a previous planning session with a follow-up prompt. It reads
// authoritative output from the plan file, but TOLERATES plan-file read failure:
// conversational continuations (gate Q&A) do not edit the plan, so the continuation
// RunResult may lack a PlanFilePath. When the plan file is unreadable, Plan is
// empty and Chat carries the model's response. Errors are returned only for runner
// failures, never for plan-file-missing.
func (p *Planner) Continue(ctx context.Context, sessionID, prompt string, stdout io.Writer) (PlanResult, error) {
	result, err := p.runner.RunContinue(ctx, sessionID, prompt, stdout)
	if err != nil {
		return PlanResult{}, fmt.Errorf("planner continue: %w", err)
	}

	planContent, planErr := ReadPlanFromRun(result)
	if planErr != nil {
		slog.Debug("planner continue: plan file unreadable (chat-only continuation)",
			"session_id", sessionID, "err", planErr)
		planContent = ""
	}

	return PlanResult{
		Plan:      planContent,
		Chat:      result.Output,
		Usage:     result.Usage,
		SessionID: result.SessionID,
	}, nil
}

// DetectPlanRevision checks whether a planner continuation produced a meaningful
// plan revision. It compares planContent (from PlanResult.Plan) against:
//   - baseline (pre-run plan file snapshot), when baselineErr == nil
//   - currentPlan otherwise
//
// Returns nil when:
//   - planContent is empty (chat-only continuation, no plan file written)
//   - planContent equals the baseline (no change written)
//   - planContent equals currentPlan (echo suppression: architect echoed user edits)
func DetectPlanRevision(planContent, baseline string, baselineErr error, currentPlan string) *RawPlan {
	if planContent == "" {
		slog.Debug("revision detection: plan content empty (chat-only continuation)")
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
	// Without this, the architect echoing user's ^E edits into its plan file
	// would be presented as a "revision" — the user's own work shown back.
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
