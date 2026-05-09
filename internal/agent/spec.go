package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RawPlan is the new pipeline's plan type: raw markdown, no parsing.
type RawPlan struct {
	Markdown string
}

// IsNewFormat returns true if the markdown starts with "# Plan" — the new format.
func IsNewFormat(md string) bool {
	return strings.HasPrefix(strings.TrimSpace(md), "# Plan")
}

// Specification is the shared contract between Planner, Worker, and Validator.
// DEPRECATED: Used by --plan <old-format> loading and internal/scheduler/.
// Not created in the new pipeline.
type Specification struct {
	SchemaVersion string   `json:"schema_version,omitempty"`
	ID            string   `json:"id,omitempty"`
	Title         string   `json:"title,omitempty"`
	Goal          string   `json:"goal"`
	Context       string   `json:"context,omitempty"`
	Steps         []string `json:"steps"`
	Acceptance    []string `json:"acceptance"`

	// Scope constrains what the worker is allowed to touch.
	Scope *Scope `json:"scope,omitempty"`

	// Constraints and risk annotations.
	Constraints []string `json:"constraints,omitempty"`
	Assumptions []string `json:"assumptions,omitempty"`
	Risks       []string `json:"risks,omitempty"`
}

// PlanOutput is the full planner response: spec + pipeline metadata.
// The planner LLM produces validation commands and artifact expectations,
// but those are not part of the spec contract — they're aids for the QA gate.
type PlanOutput struct {
	Spec Specification

	// ValidationCommands are shell commands the QA gate runs to verify work.
	ValidationCommands []ValidationCommand `json:"validation_commands,omitempty"`

	// ExpectedArtifacts lists files that should exist after execution.
	ExpectedArtifacts []string `json:"expected_artifacts,omitempty"`
}

// UnmarshalJSON handles flexible LLM output for Specification fields.
// LLMs may return "context" as an object, "steps" as structured objects, etc.
func (s *Specification) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if v, ok := raw["schema_version"]; ok {
		json.Unmarshal(v, &s.SchemaVersion)
	}
	if v, ok := raw["id"]; ok {
		json.Unmarshal(v, &s.ID)
	}
	if v, ok := raw["title"]; ok {
		json.Unmarshal(v, &s.Title)
	}
	if v, ok := raw["goal"]; ok {
		json.Unmarshal(v, &s.Goal)
	}
	if v, ok := raw["context"]; ok {
		s.Context = flexString(v)
	}
	if v, ok := raw["steps"]; ok {
		s.Steps = flexStringSlice(v)
	}
	if v, ok := raw["acceptance"]; ok {
		s.Acceptance = flexStringSlice(v)
	}
	if v, ok := raw["scope"]; ok {
		var scope Scope
		if err := json.Unmarshal(v, &scope); err == nil {
			s.Scope = &scope
		}
	}
	if v, ok := raw["constraints"]; ok {
		s.Constraints = flexStringSlice(v)
	}
	if v, ok := raw["assumptions"]; ok {
		s.Assumptions = flexStringSlice(v)
	}
	if v, ok := raw["risks"]; ok {
		s.Risks = flexStringSlice(v)
	}
	return nil
}

// UnmarshalJSON parses the full planner LLM output into spec + pipeline metadata.
func (p *PlanOutput) UnmarshalJSON(data []byte) error {
	// Parse spec fields first.
	if err := json.Unmarshal(data, &p.Spec); err != nil {
		return err
	}
	// Extract pipeline metadata from the same JSON.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["validation_commands"]; ok {
		var cmds []ValidationCommand
		if err := json.Unmarshal(v, &cmds); err == nil {
			p.ValidationCommands = cmds
		}
	}
	if v, ok := raw["expected_artifacts"]; ok {
		p.ExpectedArtifacts = flexStringSlice(v)
	}
	return nil
}

// flexString coerces a JSON value to string: if it's a string, use it directly;
// if it's an object/array, serialize it back to a compact JSON string.
func flexString(data json.RawMessage) string {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return s
	}
	// Not a string — serialize the whole value.
	return string(data)
}

// flexStringSlice coerces a JSON array where elements may be strings or objects.
// Objects are flattened to a readable string representation.
func flexStringSlice(data json.RawMessage) []string {
	// Try simple []string first.
	var ss []string
	if err := json.Unmarshal(data, &ss); err == nil {
		return ss
	}
	// Elements are mixed or all objects. Parse as []json.RawMessage.
	var elems []json.RawMessage
	if err := json.Unmarshal(data, &elems); err != nil {
		return nil
	}
	result := make([]string, 0, len(elems))
	for _, elem := range elems {
		var s string
		if err := json.Unmarshal(elem, &s); err == nil {
			result = append(result, s)
			continue
		}
		// Try common structured step formats.
		var step struct {
			Title      string `json:"title"`
			Detail     string `json:"detail"`
			ID         any    `json:"id"`
			Risk       string `json:"risk"`
			Mitigation string `json:"mitigation"`
		}
		if err := json.Unmarshal(elem, &step); err == nil {
			switch {
			case step.Title != "" && step.Detail != "":
				result = append(result, fmt.Sprintf("%s: %s", step.Title, step.Detail))
			case step.Title != "":
				result = append(result, step.Title)
			case step.Risk != "":
				if step.Mitigation != "" {
					result = append(result, fmt.Sprintf("%s (mitigation: %s)", step.Risk, step.Mitigation))
				} else {
					result = append(result, step.Risk)
				}
			default:
				result = append(result, string(elem))
			}
			continue
		}
		result = append(result, string(elem))
	}
	return result
}

// Scope defines the boundaries of a specification.
type Scope struct {
	IncludeGlobs []string `json:"include_globs,omitempty"`
	ExcludeGlobs []string `json:"exclude_globs,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

// ValidationCommand is a shell command used to verify work output.
type ValidationCommand struct {
	Command      string   `json:"command"`
	Args         []string `json:"args,omitempty"`
	Cwd          string   `json:"cwd,omitempty"`
	ExpectedExit int      `json:"expected_exit"`
}

// UnmarshalJSON allows ValidationCommand to be unmarshaled from either a plain
// string (treated as the command) or a full JSON object.
func (v *ValidationCommand) UnmarshalJSON(data []byte) error {
	// Try string first (LLMs often emit plain strings).
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		// Split "go test ./..." into Command="go", Args=["test", "./..."]
		parts := strings.Fields(s)
		if len(parts) > 0 {
			v.Command = parts[0]
			v.Args = parts[1:]
		} else {
			v.Command = s
		}
		return nil
	}
	// Fall back to full struct.
	type alias ValidationCommand
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*v = ValidationCommand(a)
	return nil
}

// BuildExecutionPrompt renders a Specification into a prompt string for worker execution.
// DEPRECATED: Used by --plan <old-format> loading. New pipeline uses BuildExecutionPromptFromPlan.
func BuildExecutionPrompt(spec Specification) string {
	prompt := fmt.Sprintf("Execute the following plan:\n\nGoal: %s\n\nSteps:\n", spec.Goal)
	for i, step := range spec.Steps {
		prompt += fmt.Sprintf("%d. %s\n", i+1, step)
	}
	if len(spec.Acceptance) > 0 {
		prompt += "\nAcceptance Criteria:\n"
		for _, criterion := range spec.Acceptance {
			prompt += fmt.Sprintf("- %s\n", criterion)
		}
	}
	return prompt
}

// BuildExecutionPromptFromPlan returns the plan markdown with a minimal preamble
// for worker execution. The worker receives the full plan verbatim.
func BuildExecutionPromptFromPlan(planMarkdown string) string {
	return "Execute the following plan sequentially. Complete each work package in order before moving to the next.\n\n" + planMarkdown
}

// WorkerValidationPrompt returns the continuation prompt for worker self-validation.
func WorkerValidationPrompt(retryBudget int) string {
	return fmt.Sprintf(`Continue your implementation session.

Validate your work against the plan you just executed.

For each "Done when" criterion in every work package:
1. Run the relevant command or inspect the file.
2. If a check fails, fix the implementation and re-run.
3. Stop after %d fix attempts.

Then run the final Verification commands from the plan.

Report your results:
- ✅ <criterion> — <evidence: command exited 0 / file content verified>
- ❌ <criterion> — <evidence: command output showing failure>
- ⚠️ <criterion> — cannot verify (explain why)

Do not claim a command passed unless you observed exit code 0.
If failures remain after retries, report them plainly. Do not hide or minimize.`, retryBudget)
}
