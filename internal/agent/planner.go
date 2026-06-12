package agent

import (
	"context"
	"fmt"
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
	// StreamFallback is true when the plan content was recovered from the CLI
	// stream result text because the plan file was not written to disk. The
	// content is best-effort and may not match the format of a plan-file report.
	StreamFallback bool
}

// Planner is the unified plan-mode agent entity. It wraps a Runner
// and a system prompt, always reading its authoritative output from the plan file.
// Worker execution does not use Planner — workers use Runner directly.
type Planner struct {
	runner harness.Runner
	system string
}

// NewPlanner creates a Planner backed by the given Runner and system prompt.
func NewPlanner(runner harness.Runner, system string) *Planner {
	return &Planner{runner: runner, system: system}
}

// ExtractPlan delegates to the underlying runner's ExtractPlan.
// Used by the orchestrator to get a plan baseline before running the planner.
func (p *Planner) ExtractPlan(ctx context.Context) (string, error) {
	return p.runner.ExtractPlan(ctx)
}

// Run executes a new planning session with the given prompt. It reads authoritative
// output from the plan file via ExtractPlan. Falls back to the CLI stream result
// text when the plan file is unreadable but the stream produced output. Returns a
// hard error only when both the plan file and stream output are unavailable.
func (p *Planner) Run(ctx context.Context, prompt string, events chan<- harness.Event) (PlanResult, error) {
	p.runner.SetEvents(events)
	p.runner.Post(prompt)

	var result harness.RunResult
	for ev := range p.runner.Receive() {
		if ev.Kind == harness.EventError {
			return PlanResult{}, fmt.Errorf("planner run: %s", ev.Text)
		}
		if ev.Kind == harness.EventUsage {
			result.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
		}
		if ev.Kind == harness.EventChunk && ev.Text != "" {
			result.Output += ev.Text
		}
		if ev.Kind == harness.EventSessionStart {
			result.SessionID = ev.SessionID
		}
	}

	planContent, planErr := p.runner.ExtractPlan(ctx)
	if planErr != nil {
		streamText := strings.TrimSpace(result.Output)
		if result.SessionID == "" || streamText == "" {
			return PlanResult{}, fmt.Errorf("planner run: read plan file: %w", planErr)
		}
		slog.Warn("plan file unreadable, falling back to stream output",
			"session_id", result.SessionID,
			"plan_err", planErr,
		)
		return PlanResult{
			Plan:           streamText,
			Chat:           result.Output,
			Usage:          result.Usage,
			SessionID:      result.SessionID,
			StreamFallback: true,
		}, nil
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
// may lack a plan file. When the plan file is unreadable, Plan is
// empty and Chat carries the model's response. Errors are returned only for runner
// failures, never for plan-file-missing.
func (p *Planner) Continue(ctx context.Context, sessionID, prompt string, events chan<- harness.Event) (PlanResult, error) {
	p.runner.SetEvents(events)
	p.runner.Post(prompt)

	var result harness.RunResult
	for ev := range p.runner.Receive() {
		if ev.Kind == harness.EventError {
			return PlanResult{}, fmt.Errorf("planner continue: %s", ev.Text)
		}
		if ev.Kind == harness.EventUsage {
			result.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
		}
		if ev.Kind == harness.EventChunk && ev.Text != "" {
			result.Output += ev.Text
		}
		if ev.Kind == harness.EventSessionStart {
			result.SessionID = ev.SessionID
		}
	}

	planContent, planErr := p.runner.ExtractPlan(ctx)
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
