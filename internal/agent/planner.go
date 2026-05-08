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

// Plan sends the user prompt to claude CLI and parses the structured plan output.
func (p *Planner) Plan(ctx context.Context, prompt string) (PlanOutput, error) {
	result, err := p.runner.RunPrint(ctx, prompt, p.cfg.SystemPrompt)
	if err != nil {
		return PlanOutput{}, err
	}
	po, err := p.ParsePlanOutput(result.Output)
	if err != nil {
		return PlanOutput{}, err
	}
	po.Usage = result.Usage
	return po, nil
}

// PlanStreaming uses RunStreaming and parses the accumulated output.
func (p *Planner) PlanStreaming(ctx context.Context, prompt string, stdout io.Writer) (PlanOutput, error) {
	result, err := p.runner.RunStreaming(ctx, prompt, p.cfg.SystemPrompt, stdout)
	if err != nil {
		return PlanOutput{}, err
	}
	po, err := p.ParsePlanOutput(result.Output)
	if err != nil {
		return PlanOutput{}, err
	}
	po.Usage = result.Usage
	return po, nil
}

// ParsePlanOutput parses a raw claude response into a PlanOutput.
// Exported for use by the TUI streaming flow.
func (p *Planner) ParsePlanOutput(raw string) (PlanOutput, error) {
	content := strings.TrimSpace(raw)

	// Strip markdown code fences if present
	content = stripCodeFences(content)

	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Result != "" {
		content = stripCodeFences(strings.TrimSpace(envelope.Result))
	}

	// Parse the full PlanOutput (spec + pipeline metadata)
	var po PlanOutput
	if err := json.Unmarshal([]byte(content), &po); err != nil {
		// If steps are objects, try flexible spec parsing
		spec, specErr := parseFlexibleSpec(content)
		if specErr != nil {
			// Last resort: try to extract a plan from markdown prose
			mdSpec, mdErr := parseMarkdownPlan(raw)
			if mdErr != nil {
				return PlanOutput{}, fmt.Errorf("parse plan output JSON: %w (raw: %s)", err, truncateRaw(content, 200))
			}
			po.Spec = mdSpec
		} else {
			po.Spec = spec
		}
	}

	if po.Spec.Goal == "" || len(po.Spec.Steps) == 0 || len(po.Spec.Acceptance) == 0 {
		return PlanOutput{}, fmt.Errorf("incomplete specification: missing goal, steps, or acceptance criteria")
	}

	return po, nil
}

// truncateRaw limits a raw string for error messages.
func truncateRaw(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// parseMarkdownPlan attempts to extract a Specification from markdown-formatted
// plan output when the model ignores the JSON output instruction.
func parseMarkdownPlan(raw string) (Specification, error) {
	lines := strings.Split(raw, "\n")
	var goal string
	var steps []string
	var acceptance []string

	type section int
	const (
		sNone section = iota
		sGoal
		sSteps
		sAcceptance
	)
	current := sNone

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		// Detect section headings (## Goal, **Goal:**, Goal:, etc.)
		switch {
		case isHeading(lower, "goal"):
			current = sGoal
			// Check for inline content: "## Goal: build a thing"
			if v := extractInlineValue(trimmed); v != "" {
				goal = v
			}
			continue
		case isHeading(lower, "steps"):
			current = sSteps
			continue
		case isHeading(lower, "acceptance"), isHeading(lower, "acceptance criteria"),
			isHeading(lower, "done when"), isHeading(lower, "success criteria"):
			current = sAcceptance
			continue
		}

		if trimmed == "" || trimmed == "---" {
			continue
		}

		switch current {
		case sGoal:
			if goal == "" {
				goal = stripListPrefix(trimmed)
			}
		case sSteps:
			if item := stripListPrefix(trimmed); item != "" {
				steps = append(steps, item)
			}
		case sAcceptance:
			if item := stripListPrefix(trimmed); item != "" {
				acceptance = append(acceptance, item)
			}
		}
	}

	if goal == "" || len(steps) == 0 || len(acceptance) == 0 {
		return Specification{}, fmt.Errorf("could not extract plan from markdown (goal=%q, steps=%d, acceptance=%d)",
			goal, len(steps), len(acceptance))
	}

	return Specification{Goal: goal, Steps: steps, Acceptance: acceptance}, nil
}

// isHeading returns true if the line is a markdown heading for the given section name.
func isHeading(lower, name string) bool {
	// "## goal", "# goal:", "**goal**", "goal:", "## goal: some inline value"
	lower = strings.TrimLeft(lower, "#* ")
	lower = strings.TrimSpace(lower)
	// Exact match: "goal"
	if lower == name {
		return true
	}
	// Prefix match with separator: "goal:" or "goal: ..."
	if strings.HasPrefix(lower, name+":") || strings.HasPrefix(lower, name+" ") {
		return true
	}
	return false
}

// extractInlineValue extracts text after ":" or the heading marker on the same line.
func extractInlineValue(line string) string {
	// Strip markdown heading prefixes
	line = strings.TrimLeft(line, "# ")
	// Strip bold markers
	line = strings.ReplaceAll(line, "**", "")
	// Find colon separator
	idx := strings.Index(line, ":")
	if idx >= 0 && idx < len(line)-1 {
		return strings.TrimSpace(line[idx+1:])
	}
	return ""
}

// stripListPrefix removes "- ", "* ", "1. ", etc. from a line.
func stripListPrefix(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return ""
	}
	// Numbered list: "1. ", "2. "
	for i, c := range s {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '.' && i > 0 && i < len(s)-1 && s[i+1] == ' ' {
			return strings.TrimSpace(s[i+2:])
		}
		break
	}
	// Bullet: "- " or "* "
	if (s[0] == '-' || s[0] == '*') && len(s) > 2 && s[1] == ' ' {
		return strings.TrimSpace(s[2:])
	}
	return s
}

// ParseSpec parses a raw claude response into a Specification.
// Exported for backward compatibility.
func (p *Planner) ParseSpec(raw string) (Specification, error) {
	po, err := p.ParsePlanOutput(raw)
	if err != nil {
		return Specification{}, err
	}
	return po.Spec, nil
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
