package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
)

// Planner generates a Specification from a user prompt via Claude Code in plan mode.
type Planner struct {
	runner harness.CLIRunner
	cfg    *config.PlannerConfig
}

func New(runner harness.CLIRunner, cfg *config.PlannerConfig) *Planner {
	return &Planner{runner: runner, cfg: cfg}
}

// Plan sends the user prompt to claude CLI and parses the structured specification.
func (p *Planner) Plan(ctx context.Context, prompt string) (types.Result[types.Specification], error) {
	output, err := p.runner.RunPrint(ctx, prompt, p.cfg.SystemPrompt)
	if err != nil {
		return types.Fail[types.Specification](err), err
	}

	// claude --output-format json wraps output in {"type":"result","result":"..."}
	content := output
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Result != "" {
		content = envelope.Result
	}

	var spec types.Specification
	if err := json.Unmarshal([]byte(content), &spec); err != nil {
		return types.Fail[types.Specification](
			fmt.Errorf("parse specification JSON: %w (raw: %s)", err, content),
		), nil
	}

	if spec.Goal == "" || len(spec.Steps) == 0 || len(spec.Acceptance) == 0 {
		return types.Fail[types.Specification](
			fmt.Errorf("incomplete specification: missing goal, steps, or acceptance criteria"),
		), nil
	}

	return types.Ok(spec), nil
}

// PlanStreaming uses RunStreaming and parses the accumulated output.
func (p *Planner) PlanStreaming(ctx context.Context, prompt string, stdout io.Writer) (types.Specification, error) {
	output, err := p.runner.RunStreaming(ctx, prompt, p.cfg.SystemPrompt, stdout)
	if err != nil {
		return types.Specification{}, err
	}
	return p.ParseSpec(output)
}

// ParseSpec parses a raw claude response into a Specification.
// Exported for use by the TUI streaming flow.
func (p *Planner) ParseSpec(raw string) (types.Specification, error) {
	content := raw
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Result != "" {
		content = envelope.Result
	}

	var spec types.Specification
	if err := json.Unmarshal([]byte(content), &spec); err != nil {
		return types.Specification{}, fmt.Errorf("parse specification JSON: %w (raw: %s)", err, content)
	}

	if spec.Goal == "" || len(spec.Steps) == 0 || len(spec.Acceptance) == 0 {
		return types.Specification{}, fmt.Errorf("incomplete specification: missing goal, steps, or acceptance criteria")
	}

	return spec, nil
}
