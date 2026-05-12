package agent

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// Critic reviews an architect's plan against the actual codebase and produces
// a structured report of execution blockers.
type Critic struct {
	runner harness.CLIRunner
	cfg    config.CriticConfig
}

// NewCritic creates a Critic backed by the given CLIRunner.
func NewCritic(runner harness.CLIRunner, cfg config.CriticConfig) *Critic {
	return &Critic{runner: runner, cfg: cfg}
}

// BlockerSummary counts blockers by severity.
type BlockerSummary struct {
	High   int
	Medium int
	Low    int
}

// Total returns the total number of blockers.
func (b BlockerSummary) Total() int { return b.High + b.Medium + b.Low }

// CriticReport is the result of a plan review.
type CriticReport struct {
	Markdown string
	Blockers BlockerSummary
}

// ReviewStreaming reviews the plan against the codebase and streams output.
func (c *Critic) ReviewStreaming(ctx context.Context, userPrompt, planMarkdown string, stdout io.Writer) (CriticReport, harness.TokenUsage, string, error) {
	prompt := fmt.Sprintf("<user_request>\n%s\n</user_request>\n\n<implementation_plan>\n%s\n</implementation_plan>\n\nReview the implementation plan above against the actual codebase. Produce a Critic Report.",
		userPrompt, planMarkdown)
	result, err := c.runner.RunStreaming(ctx, prompt, c.cfg.SystemPrompt, stdout)
	if err != nil {
		return CriticReport{}, harness.TokenUsage{}, "", fmt.Errorf("critic review: %w", err)
	}

	report := CriticReport{
		Markdown: result.Output,
		Blockers: parseSeverityCounts(result.Output),
	}
	return report, result.Usage, result.SessionID, nil
}

// severityPattern matches **Severity**: High|Medium|Low in critic output.
var severityPattern = regexp.MustCompile(`\*\*Severity\*\*:\s*(High|Medium|Low)`)

// parseSeverityCounts extracts blocker severity counts from a critic report.
func parseSeverityCounts(markdown string) BlockerSummary {
	var summary BlockerSummary
	for _, match := range severityPattern.FindAllStringSubmatch(markdown, -1) {
		switch strings.TrimSpace(match[1]) {
		case "High":
			summary.High++
		case "Medium":
			summary.Medium++
		case "Low":
			summary.Low++
		}
	}
	return summary
}
