package agent

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// Researcher generates a draft implementation plan from a user prompt via
// Claude Code. The researcher has full tool access and explores the codebase
// deeply, producing a markdown draft that the Planner refines.
type Researcher struct {
	runner harness.CLIRunner
	cfg    config.ResearcherConfig
}

// NewResearcher creates a Researcher backed by the given CLIRunner.
func NewResearcher(runner harness.CLIRunner, cfg config.ResearcherConfig) *Researcher {
	return &Researcher{runner: runner, cfg: cfg}
}

// Research sends the user prompt to the CLI and returns the raw markdown output.
func (r *Researcher) Research(ctx context.Context, prompt string) (RawPlan, harness.TokenUsage, string, error) {
	result, err := r.runner.RunPrint(ctx, prompt, r.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", err
	}
	return r.parseResearchResultWithRecovery(result)
}

// ResearchStreaming uses RunStreaming and returns the raw markdown output.
func (r *Researcher) ResearchStreaming(ctx context.Context, prompt string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	result, err := r.runner.RunStreaming(ctx, prompt, r.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", err
	}
	return r.parseResearchResultWithRecovery(result)
}

// parseResearchResultWithRecovery attempts plan-file side-channel recovery
// when the researcher output lacks any markdown heading (suggesting the model
// wrote to a plan file instead of returning text).
func (r *Researcher) parseResearchResultWithRecovery(result harness.RunResult) (RawPlan, harness.TokenUsage, string, error) {
	md := strings.TrimSpace(stripCodeFences(result.Output))

	if strings.Contains(md, "#") {
		return RawPlan{Markdown: md}, result.Usage, result.SessionID, nil
	}

	if result.SessionID == "" {
		return RawPlan{Markdown: md}, result.Usage, result.SessionID, nil
	}

	recovered, recoverErr := recoverPlanFromSession(result.SessionID)
	if recoverErr != nil {
		slog.Debug("researcher plan file recovery failed", "session_id", result.SessionID, "err", recoverErr)
		return RawPlan{Markdown: md}, result.Usage, result.SessionID, nil
	}

	slog.Info("researcher recovered plan from Claude CLI plan file", "session_id", result.SessionID)
	recoveredMD := strings.TrimSpace(stripCodeFences(recovered))
	return RawPlan{Markdown: recoveredMD}, result.Usage, result.SessionID, nil
}
