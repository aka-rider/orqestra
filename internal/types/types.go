package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Specification is the shared contract between Planner, Worker, and Validator.
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

	// ValidationCommands are shell commands the validator can run to check work.
	ValidationCommands []ValidationCommand `json:"validation_commands,omitempty"`

	// AllowedOperations restricts what the worker harness may do.
	AllowedOperations []string `json:"allowed_operations,omitempty"`

	// ExpectedArtifacts lists files or outputs that should exist after execution.
	ExpectedArtifacts []string `json:"expected_artifacts,omitempty"`
}

// UnmarshalJSON handles flexible LLM output for Specification fields.
// LLMs may return "context" as an object, "steps" as structured objects, etc.
func (s *Specification) UnmarshalJSON(data []byte) error {
	// Use a raw map to handle heterogeneous field types.
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
	if v, ok := raw["validation_commands"]; ok {
		var cmds []ValidationCommand
		if err := json.Unmarshal(v, &cmds); err == nil {
			s.ValidationCommands = cmds
		}
	}
	if v, ok := raw["allowed_operations"]; ok {
		s.AllowedOperations = flexStringSlice(v)
	}
	if v, ok := raw["expected_artifacts"]; ok {
		s.ExpectedArtifacts = flexStringSlice(v)
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

// ValidationReport is the unified result shape for all validators.
type ValidationReport struct {
	SchemaVersion string   `json:"schema_version"`
	Verdict       Verdict  `json:"verdict"`
	Summary       string   `json:"summary"`
	Issues        []Issue  `json:"issues,omitempty"`
	Suggestions   []string `json:"suggestions,omitempty"`
}

// Verdict represents the overall outcome of validation.
type Verdict string

const (
	VerdictPass Verdict = "pass"
	VerdictWarn Verdict = "warn"
	VerdictFail Verdict = "fail"
)

// Issue is a single problem found during validation.
type Issue struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Location string   `json:"location,omitempty"`
}

// Severity indicates how critical an issue is.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// DeriveVerdict computes the overall verdict from a set of issues.
func DeriveVerdict(issues []Issue) Verdict {
	verdict := VerdictPass
	for _, issue := range issues {
		switch issue.Severity {
		case SeverityError:
			return VerdictFail
		case SeverityWarning:
			verdict = VerdictWarn
		}
	}
	return verdict
}

// ValidationCommandResult captures the outcome of running a validation command.
type ValidationCommandResult struct {
	Command      string   `json:"command"`
	Args         []string `json:"args,omitempty"`
	Cwd          string   `json:"cwd,omitempty"`
	ExpectedExit int      `json:"expected_exit"`
	ActualExit   int      `json:"actual_exit"`
	Stdout       string   `json:"stdout,omitempty"`
	Stderr       string   `json:"stderr,omitempty"`
	Passed       bool     `json:"passed"`
}

// FailedCriterion describes an acceptance criterion that was not met.
type FailedCriterion struct {
	Criterion string `json:"criterion"`
	Reason    string `json:"reason"`
}

// WorkPackage is a single unit of work assigned by the Project Manager.
// Each package is executed by one worker session independently.
type WorkPackage struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Steps       []string `json:"steps"`
	Acceptance  []string `json:"acceptance"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
}

// ProjectPlan is the PM's decomposition of a Specification into parallel worker tasks.
type ProjectPlan struct {
	SchemaVersion string        `json:"schema_version"`
	Packages      []WorkPackage `json:"packages"`
}

// ToSpecification converts a WorkPackage into a Specification suitable for a
// single worker session, inheriting context from the parent spec.
func (wp WorkPackage) ToSpecification(parent Specification) Specification {
	return Specification{
		SchemaVersion: parent.SchemaVersion,
		ID:            wp.ID,
		Title:         wp.Title,
		Goal:          wp.Title,
		Context:       parent.Context,
		Steps:         wp.Steps,
		Acceptance:    wp.Acceptance,
		Scope:         parent.Scope,
		Constraints:   wp.Constraints,
	}
}

// BuildExecutionPrompt renders a Specification into a prompt string for worker execution.
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

// FormatValidationFeedback renders a ValidationReport into text feedback for re-planning.
func FormatValidationFeedback(report *ValidationReport) string {
	result := fmt.Sprintf("Verdict: %s\nSummary: %s\n", report.Verdict, report.Summary)
	if len(report.Issues) > 0 {
		result += "Issues:\n"
		for _, issue := range report.Issues {
			result += fmt.Sprintf("  [%s] %s: %s\n", issue.Severity, issue.ID, issue.Message)
		}
	}
	if len(report.Suggestions) > 0 {
		result += "Suggestions:\n"
		for _, s := range report.Suggestions {
			result += fmt.Sprintf("  - %s\n", s)
		}
	}
	return result
}

// TopoWaves sorts work packages into dependency waves using Kahn's algorithm.
// Each wave contains packages whose dependencies are all in prior waves.
func TopoWaves(packages []WorkPackage) [][]WorkPackage {
	idx := make(map[string]int, len(packages))
	for i, pkg := range packages {
		idx[pkg.ID] = i
	}

	inDegree := make([]int, len(packages))
	for i := range packages {
		for range packages[i].DependsOn {
			inDegree[i]++
		}
	}

	var queue []int
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	var waves [][]WorkPackage
	for len(queue) > 0 {
		wave := make([]WorkPackage, len(queue))
		for i, qi := range queue {
			wave[i] = packages[qi]
		}
		waves = append(waves, wave)

		var nextQueue []int
		for _, qi := range queue {
			curID := packages[qi].ID
			for i, pkg := range packages {
				for _, dep := range pkg.DependsOn {
					if dep == curID {
						inDegree[i]--
						if inDegree[i] == 0 {
							nextQueue = append(nextQueue, i)
						}
					}
				}
			}
		}
		queue = nextQueue
	}

	return waves
}
