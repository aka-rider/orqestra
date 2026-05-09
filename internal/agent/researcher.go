package agent

import (
	"context"
	"io"
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
func (r *Researcher) Research(ctx context.Context, prompt string) (RawPlan, harness.TokenUsage, error) {
	result, err := r.runner.RunPrint(ctx, prompt, r.cfg.SystemPrompt)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, err
	}
	return RawPlan{
		Markdown: strings.TrimSpace(stripCodeFences(result.Output)),
	}, result.Usage, nil
}

// ResearchStreaming uses RunStreaming and returns the raw markdown output.
func (r *Researcher) ResearchStreaming(ctx context.Context, prompt string, stdout io.Writer) (RawPlan, harness.TokenUsage, error) {
	result, err := r.runner.RunStreaming(ctx, prompt, r.cfg.SystemPrompt, stdout)
	if err != nil {
		return RawPlan{}, harness.TokenUsage{}, err
	}
	return RawPlan{
		Markdown: strings.TrimSpace(stripCodeFences(result.Output)),
	}, result.Usage, nil
}
