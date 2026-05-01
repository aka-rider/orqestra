package types

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

// Scope defines the boundaries of a specification.
type Scope struct {
	IncludeGlobs []string `json:"include_globs,omitempty"`
	ExcludeGlobs []string `json:"exclude_globs,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

// ValidationCommand is a shell command used to verify work output.
type ValidationCommand struct {
	Command      string `json:"command"`
	Args         []string `json:"args,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	ExpectedExit int    `json:"expected_exit"`
}

// ValidationReport is the unified result shape for all validators.
type ValidationReport struct {
	SchemaVersion string  `json:"schema_version"`
	Verdict       Verdict `json:"verdict"`
	Summary       string  `json:"summary"`
	Issues        []Issue `json:"issues,omitempty"`
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
	Command      string `json:"command"`
	Args         []string `json:"args,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	ExpectedExit int    `json:"expected_exit"`
	ActualExit   int    `json:"actual_exit"`
	Stdout       string `json:"stdout,omitempty"`
	Stderr       string `json:"stderr,omitempty"`
	Passed       bool   `json:"passed"`
}

// Result represents either a success value or an error.
type Result[T any] struct {
	Value T
	Err   error
}

func Ok[T any](v T) Result[T] {
	return Result[T]{Value: v}
}

func Fail[T any](err error) Result[T] {
	return Result[T]{Err: err}
}

func (r Result[T]) IsOk() bool {
	return r.Err == nil
}
