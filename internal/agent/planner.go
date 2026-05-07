package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// Planner generates a Specification from a user prompt via Claude Code in plan mode.
type Planner struct {
	runner harness.CLIRunner
	cfg    *config.PlannerConfig
}

// NewPlanner creates a Planner backed by the given CLIRunner.
func NewPlanner(runner harness.CLIRunner, cfg *config.PlannerConfig) *Planner {
	return &Planner{runner: runner, cfg: cfg}
}

// Plan sends the user prompt to claude CLI and parses the structured specification.
func (p *Planner) Plan(ctx context.Context, prompt string) (Specification, error) {
	result, err := p.runner.RunPrint(ctx, prompt, p.cfg.SystemPrompt)
	if err != nil {
		return Specification{}, err
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

	var spec Specification
	if err := json.Unmarshal([]byte(content), &spec); err != nil {
		return Specification{}, fmt.Errorf("parse specification JSON: %w (raw: %s)", err, content)
	}

	if spec.Goal == "" || len(spec.Steps) == 0 || len(spec.Acceptance) == 0 {
		return Specification{}, fmt.Errorf("incomplete specification: missing goal, steps, or acceptance criteria")
	}

	return spec, nil
}

// PlanStreaming uses RunStreaming and parses the accumulated output.
func (p *Planner) PlanStreaming(ctx context.Context, prompt string, stdout io.Writer) (Specification, error) {
	result, err := p.runner.RunStreaming(ctx, prompt, p.cfg.SystemPrompt, stdout)
	if err != nil {
		return Specification{}, err
	}
	return p.ParseSpec(result.Output)
}

// ParseSpec parses a raw claude response into a Specification.
// Exported for use by the TUI streaming flow.
func (p *Planner) ParseSpec(raw string) (Specification, error) {
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
	var spec Specification
	if err := json.Unmarshal([]byte(content), &spec); err != nil {
		// If steps are objects, parse with a flexible intermediate type
		spec, err = parseFlexibleSpec(content)
		if err != nil {
			return Specification{}, fmt.Errorf("parse specification JSON: %w (raw: %s)", err, content)
		}
	}

	if spec.Goal == "" || len(spec.Steps) == 0 || len(spec.Acceptance) == 0 {
		return Specification{}, fmt.Errorf("incomplete specification: missing goal, steps, or acceptance criteria")
	}

	return spec, nil
}

// parseFlexibleSpec handles LLM responses where steps are objects instead of strings.
func parseFlexibleSpec(content string) (Specification, error) {
	var raw struct {
		Goal       string            `json:"goal"`
		Steps      []json.RawMessage `json:"steps"`
		Acceptance []string          `json:"acceptance"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return Specification{}, err
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

	return Specification{
		Goal:       raw.Goal,
		Steps:      steps,
		Acceptance: raw.Acceptance,
	}, nil
}

// stripCodeFences removes ```json ... ``` or ``` ... ``` wrapping from a string.
// Also handles the case where text commentary precedes the code fence.
func stripCodeFences(s string) string {
	// If it starts with a code fence, strip directly.
	if strings.HasPrefix(s, "```") {
		idx := strings.Index(s, "\n")
		if idx == -1 {
			return s
		}
		s = s[idx+1:]
		if last := strings.LastIndex(s, "```"); last >= 0 {
			s = s[:last]
		}
		return strings.TrimSpace(s)
	}

	// Look for a code fence anywhere in the string (text + ```json\n...\n```)
	fenceStart := strings.Index(s, "```json")
	if fenceStart == -1 {
		fenceStart = strings.Index(s, "```JSON")
	}
	if fenceStart == -1 {
		fenceStart = strings.Index(s, "```\n{")
	}
	if fenceStart >= 0 {
		rest := s[fenceStart:]
		idx := strings.Index(rest, "\n")
		if idx >= 0 {
			rest = rest[idx+1:]
			if last := strings.LastIndex(rest, "```"); last >= 0 {
				rest = rest[:last]
			}
			return strings.TrimSpace(rest)
		}
	}

	// No code fence found — try to extract raw JSON object.
	// Find the first '{' and last '}' to extract the JSON payload.
	first := strings.Index(s, "{")
	last := strings.LastIndex(s, "}")
	if first >= 0 && last > first {
		return s[first : last+1]
	}

	return s
}
