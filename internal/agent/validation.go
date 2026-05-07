package agent

import "fmt"

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
