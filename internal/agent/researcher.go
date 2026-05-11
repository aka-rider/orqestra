package agent

import (
	"context"
	"fmt"
	"io"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// Researcher generates a draft implementation plan from a user prompt via
// Claude Code. The researcher has full tool access and explores the codebase
// deeply, producing a markdown draft that the Architect refines.
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
	return r.extractResearchDraft(result)
}

// ResearchStreaming uses RunStreaming and returns the raw markdown output.
func (r *Researcher) ResearchStreaming(ctx context.Context, prompt string, stdout io.Writer) (RawPlan, harness.TokenUsage, string, error) {
	result, err := r.runner.RunStreaming(ctx, prompt, r.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", err
	}
	return r.extractResearchDraft(result)
}

// extractResearchDraft reads the research draft from the Claude CLI plan file.
// No structural validation — the researcher's sections (## Goal, ## Draft Steps, etc.)
// differ from the architect's and are not validated here.
func (r *Researcher) extractResearchDraft(result harness.RunResult) (RawPlan, harness.TokenUsage, string, error) {
	content, err := ReadPlanFromRun(result)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, "", fmt.Errorf("extract research draft: %w", err)
	}
	return RawPlan{Markdown: content}, result.Usage, result.SessionID, nil
}
