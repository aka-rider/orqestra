package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

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
func (p *Planner) Plan(ctx context.Context, prompt string) (types.Specification, error) {
	result, err := p.runner.RunPrint(ctx, prompt, p.cfg.SystemPrompt)
	if err != nil {
		return types.Specification{}, err
	}

	// claude --output-format json wraps output in {"type":"result","result":"..."}
	content := strings.TrimSpace(result.Output)
	content = stripCodeFences(content)
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Result != "" {
		content = stripCodeFences(strings.TrimSpace(envelope.Result))
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

// PlanStreaming uses RunStreaming and parses the accumulated output.
func (p *Planner) PlanStreaming(ctx context.Context, prompt string, stdout io.Writer) (types.Specification, error) {
	result, err := p.runner.RunStreaming(ctx, prompt, p.cfg.SystemPrompt, stdout)
	if err != nil {
		return types.Specification{}, err
	}
	return p.ParseSpec(result.Output)
}

// ParseSpec parses a raw claude response into a Specification.
// Exported for use by the TUI streaming flow.
func (p *Planner) ParseSpec(raw string) (types.Specification, error) {
	content := strings.TrimSpace(raw)

	// Strip markdown code fences if present
	content = stripCodeFences(content)

	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Result != "" {
		content = stripCodeFences(strings.TrimSpace(envelope.Result))
	}

	// First try direct unmarshal (steps as []string)
	var spec types.Specification
	if err := json.Unmarshal([]byte(content), &spec); err != nil {
		// If steps are objects, parse with a flexible intermediate type
		spec, err = parseFlexibleSpec(content)
		if err != nil {
			return types.Specification{}, fmt.Errorf("parse specification JSON: %w (raw: %s)", err, content)
		}
	}

	if spec.Goal == "" || len(spec.Steps) == 0 || len(spec.Acceptance) == 0 {
		return types.Specification{}, fmt.Errorf("incomplete specification: missing goal, steps, or acceptance criteria")
	}

	return spec, nil
}

// parseFlexibleSpec handles LLM responses where steps are objects instead of strings.
func parseFlexibleSpec(content string) (types.Specification, error) {
	var raw struct {
		Goal       string            `json:"goal"`
		Steps      []json.RawMessage `json:"steps"`
		Acceptance []string          `json:"acceptance"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return types.Specification{}, err
	}

	var steps []string
	for _, s := range raw.Steps {
		// Try string first
		var str string
		if err := json.Unmarshal(s, &str); err == nil {
			steps = append(steps, str)
			continue
		}
		// Try object with "action" field
		var obj struct {
			Action      string `json:"action"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(s, &obj); err == nil {
			if obj.Action != "" {
				steps = append(steps, obj.Action)
			} else if obj.Description != "" {
				steps = append(steps, obj.Description)
			}
			continue
		}
		// Last resort: use raw string representation
		steps = append(steps, string(s))
	}

	return types.Specification{
		Goal:       raw.Goal,
		Steps:      steps,
		Acceptance: raw.Acceptance,
	}, nil
}

// stripCodeFences removes ```json ... ``` or ``` ... ``` wrapping from a string.
func stripCodeFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Remove opening fence line
	idx := strings.Index(s, "\n")
	if idx == -1 {
		return s
	}
	s = s[idx+1:]
	// Remove closing fence
	if last := strings.LastIndex(s, "```"); last >= 0 {
		s = s[:last]
	}
	return strings.TrimSpace(s)
}
