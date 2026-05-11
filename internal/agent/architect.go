package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	return a.parsePlanResultWithRecovery(result)
}

// RefineStreaming is like Refine but streams output.
func (a *Architect) RefineStreaming(ctx context.Context, researcherDraft string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := "Refine this researcher draft into a final implementation plan:\n\n" + researcherDraft
	result, err := a.runner.RunStreaming(ctx, prompt, a.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect refine streaming: %w", err)
	}
	return a.parsePlanResultWithRecovery(result)
}

// RefineWithComments takes a previous plan and human comments, producing a revised plan.
func (a *Architect) RefineWithComments(ctx context.Context, previousPlan, comments string) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
	result, err := a.runner.RunPrint(ctx, prompt, a.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect refine with comments: %w", err)
	}
	return a.parsePlanResultWithRecovery(result)
}

// RefineWithCommentsStreaming is like RefineWithComments but streams output.
func (a *Architect) RefineWithCommentsStreaming(ctx context.Context, previousPlan, comments string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
	result, err := a.runner.RunStreaming(ctx, prompt, a.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect refine with comments streaming: %w", err)
	}
	return a.parsePlanResultWithRecovery(result)
}

// continuePromptTemplate is the prompt used when resuming an architect session.
// It includes the current plan as ground truth and the reviewer's message.
const continuePromptTemplate = `The current implementation plan is below. The reviewer sent a message.

<current_plan>
%s
</current_plan>

<reviewer_message>
%s
</reviewer_message>

If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
If the reviewer requests changes, revise the plan and output the complete updated plan.
Begin with your response. Then, ONLY if you changed the plan, output the full revised plan starting with "# Plan".
Do NOT output "# Plan" unless you actually changed the plan.`

// ContinueSession resumes the architect's session to handle a reviewer comment.
// It uses RunContinue (--resume) to maintain the full conversation context.
// The method writes the revised plan directly to planPath if the architect makes changes.
// Returns the chat response text, token usage, and error.
// The caller should check git status on planPath to detect whether the plan was revised.
func (a *Architect) ContinueSession(ctx context.Context, sessionID, planPath, comment string, stdout io.Writer) (string, harness.TokenUsage, error) {
	cr, ok := a.runner.(harness.ContinuableRunner)
	if !ok {
		return "", harness.TokenUsage{}, fmt.Errorf("architect runner does not support session continuation")
	}

	// Read current plan from disk as ground truth
	planData, err := os.ReadFile(planPath)
	if err != nil {
		return "", harness.TokenUsage{}, fmt.Errorf("read plan for continuation: %w", err)
	}
	currentPlan := string(planData)

	prompt := fmt.Sprintf(continuePromptTemplate, currentPlan, comment)
	result, err := cr.RunContinue(ctx, sessionID, prompt, stdout)
	if err != nil {
		return "", harness.TokenUsage{}, fmt.Errorf("architect continue session: %w", err)
	}

	return result.Output, result.Usage, nil
}

// parsePlanResult extracts markdown from a run result and performs basic sanity checks.
func (a *Architect) parsePlanResult(result harness.RunResult) (RawPlan, harness.TokenUsage, string, error) {
	md := strings.TrimSpace(stripCodeFences(result.Output))

	// Basic sanity: must start with "# Plan" and contain "## Work Packages"
	if !strings.HasPrefix(md, "# Plan") {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect output does not start with '# Plan' (got: %s)", truncateRaw(md, 100))
	}
	if !strings.Contains(md, "## Work Packages") {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("architect output missing '## Work Packages' section")
	}

	return RawPlan{
		Markdown: md,
	}, result.Usage, result.SessionID, nil
}

// parsePlanResultWithRecovery wraps parsePlanResult with plan-file side-channel recovery.
// When parsePlanResult fails and the result has a session ID, it attempts to read
// the plan from the Claude CLI plan file (written via permission_mode: plan).
func (a *Architect) parsePlanResultWithRecovery(result harness.RunResult) (RawPlan, harness.TokenUsage, string, error) {
	plan, usage, sessionID, parseErr := a.parsePlanResult(result)
	if parseErr == nil {
		return plan, usage, sessionID, nil
	}

	if result.SessionID == "" {
		return RawPlan{}, harness.TokenUsage{}, "", parseErr
	}

	recovered, recoverErr := recoverPlanFromSession(result.SessionID)
	if recoverErr != nil {
		slog.Debug("plan file recovery failed", "session_id", result.SessionID, "err", recoverErr)
		return RawPlan{}, harness.TokenUsage{}, "", parseErr
	}

	result.Output = recovered
	slog.Info("recovered plan from Claude CLI plan file", "session_id", result.SessionID)
	return a.parsePlanResult(result)
}

// recoverPlanFromSession reads a plan from the Claude CLI plan file
// referenced in the session JSONL's plan_mode attachment.
func recoverPlanFromSession(sessionID string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get cwd: %w", err)
	}

	jsonlPath, err := harness.ResolveSessionLogPath(cwd, sessionID)
	if err != nil {
		return "", fmt.Errorf("resolve session log: %w", err)
	}

	planFilePath, err := harness.ExtractPlanFilePath(jsonlPath)
	if err != nil {
		return "", fmt.Errorf("extract plan file path: %w", err)
	}

	// Security gate: plan file must reside under ~/.claude/plans/
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	allowedPrefix := filepath.Join(home, ".claude", "plans") + string(filepath.Separator)
	absPath, err := filepath.Abs(planFilePath)
	if err != nil {
		return "", fmt.Errorf("resolve plan file path: %w", err)
	}
	if !strings.HasPrefix(absPath, allowedPrefix) {
		return "", fmt.Errorf("plan file %q is outside allowed directory %q", absPath, allowedPrefix)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read plan file %q: %w", absPath, err)
	}
	return string(data), nil
}

// truncateRaw limits a raw string for error messages.
func truncateRaw(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
