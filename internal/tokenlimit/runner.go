package tokenlimit

import (
	"context"
	"io"

	"github.com/xiii/orqestra/internal/harness"
)

// LimitedRunner wraps a CLIRunner with token budget enforcement.
// It checks the budget before each call and records usage after.
type LimitedRunner struct {
	inner   harness.CLIRunner
	limiter *Limiter
	model   string // underlying model string for budget tracking
	agentID string // identifies which agent/stage is consuming tokens
}

// NewLimitedRunner wraps a CLIRunner with token budget enforcement.
func NewLimitedRunner(inner harness.CLIRunner, limiter *Limiter, model, agentID string) *LimitedRunner {
	return &LimitedRunner{
		inner:   inner,
		limiter: limiter,
		model:   model,
		agentID: agentID,
	}
}

func (r *LimitedRunner) RunPrint(ctx context.Context, prompt, systemPrompt string) (harness.RunResult, error) {
	if err := r.limiter.Check(ctx, r.model, r.agentID); err != nil {
		return harness.RunResult{}, err
	}

	result, innerErr := r.inner.RunPrint(ctx, prompt, systemPrompt)

	if result.Usage != nil && result.Usage.TotalTokens > 0 {
		if budgetErr := r.limiter.Record(ctx, r.model, r.agentID, result.Usage.TotalTokens); budgetErr != nil {
			// Record succeeded (tokens are persisted), but budget is now exceeded.
			// If the inner call also errored, return the inner error — it is the primary failure.
			if innerErr != nil {
				return result, innerErr
			}
			return result, budgetErr
		}
	}

	return result, innerErr
}

func (r *LimitedRunner) RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (harness.RunResult, error) {
	if err := r.limiter.Check(ctx, r.model, r.agentID); err != nil {
		return harness.RunResult{}, err
	}

	result, innerErr := r.inner.RunStreaming(ctx, prompt, systemPrompt, stdout)

	if result.Usage != nil && result.Usage.TotalTokens > 0 {
		if budgetErr := r.limiter.Record(ctx, r.model, r.agentID, result.Usage.TotalTokens); budgetErr != nil {
			if innerErr != nil {
				return result, innerErr
			}
			return result, budgetErr
		}
	}

	return result, innerErr
}
