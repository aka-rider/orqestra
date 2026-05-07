package agent

import (
	"fmt"

	"github.com/xiii/orqestra/internal/harness"
)

// ValidationReport is the unified result shape for all validators.
type ValidationReport struct {
	SchemaVersion string   `json:"schema_version"`
	Verdict       Verdict  `json:"verdict"`
	Summary       string   `json:"summary"`
	Issues        []Issue  `json:"issues,omitempty"`
	Suggestions   []string `json:"suggestions,omitempty"`

	// Usage is populated after parsing from the harness RunResult, not from LLM output.
	Usage *harness.TokenUsage `json:"-"`
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
	ID       string `json:"id"`
	Blocking bool   `json:"blocking"`
	Message  string `json:"message"`
	Location string `json:"location,omitempty"`
}

// DeriveVerdict computes the overall verdict from a set of issues.
// Any blocking issue → fail. Non-blocking issues only → warn. No issues → pass.
func DeriveVerdict(issues []Issue) Verdict {
	verdict := VerdictPass
	for _, issue := range issues {
		if issue.Blocking {
			return VerdictFail
		}
		verdict = VerdictWarn
	}
	return verdict
}

// FormatValidationFeedback renders a ValidationReport into text feedback for re-planning.
func FormatValidationFeedback(report *ValidationReport) string {
	result := fmt.Sprintf("Verdict: %s\nSummary: %s\n", report.Verdict, report.Summary)
	if len(report.Issues) > 0 {
		result += "Issues:\n"
		for _, issue := range report.Issues {
			tag := "info"
			if issue.Blocking {
				tag = "blocker"
			}
			result += fmt.Sprintf("  [%s] %s: %s\n", tag, issue.ID, issue.Message)
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
