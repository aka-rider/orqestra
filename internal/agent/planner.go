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
	return p.parsePlanResultWithRecovery(result)
}

// RefineStreaming is like Refine but streams output.
func (p *Planner) RefineStreaming(ctx context.Context, researcherDraft string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := "Refine this researcher draft into a final implementation plan:\n\n" + researcherDraft
	result, err := p.runner.RunStreaming(ctx, prompt, p.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner refine streaming: %w", err)
	}
	return p.parsePlanResultWithRecovery(result)
}

// RefineWithComments takes a previous plan and human comments, producing a revised plan.
func (p *Planner) RefineWithComments(ctx context.Context, previousPlan, comments string) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
	result, err := p.runner.RunPrint(ctx, prompt, p.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner refine with comments: %w", err)
	}
	return p.parsePlanResultWithRecovery(result)
}

// RefineWithCommentsStreaming is like RefineWithComments but streams output.
func (p *Planner) RefineWithCommentsStreaming(ctx context.Context, previousPlan, comments string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
	result, err := p.runner.RunStreaming(ctx, prompt, p.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("planner refine with comments streaming: %w", err)
	}
	return p.parsePlanResultWithRecovery(result)
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

// parsePlanResultWithRecovery wraps parsePlanResult with plan-file side-channel recovery.
// When parsePlanResult fails and the result has a session ID, it attempts to read
// the plan from the Claude CLI plan file (written via permission_mode: plan).
func (p *Planner) parsePlanResultWithRecovery(result harness.RunResult) (RawPlan, harness.TokenUsage, string, error) {
	plan, usage, sessionID, parseErr := p.parsePlanResult(result)
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
	return p.parsePlanResult(result)
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
