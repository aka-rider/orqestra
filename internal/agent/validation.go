package agent

import (
	"fmt"
	"strings"
)

// Validation markers are the protocol contract between WorkerValidationPrompt
// and ParseValidationOutput. The LLM is instructed to emit these characters at
// the start of each check line; the parser recognises them to build a typed
// result. They are NOT display icons — the TUI owns its own rendering constants.
const (
	MarkerPass = "✓"
	MarkerFail = "✕"
	MarkerWarn = "⚠"
)

// CheckOutcome classifies a single validation check result.
type CheckOutcome string

const (
	CheckPassed  CheckOutcome = "passed"
	CheckFailed  CheckOutcome = "failed"
	CheckSkipped CheckOutcome = "skipped"
)

// CheckResult is one parsed check line from validation output.
type CheckResult struct {
	Outcome   CheckOutcome
	Criterion string
}

// ValidationOutput is the structured result of parsing raw LLM validation text.
type ValidationOutput struct {
	Checks  []CheckResult
	Verdict Verdict
	Raw     string // original text preserved for display and artifact storage
}

// ParseValidationOutput is a best-effort parser for raw LLM validation text.
// It scans for marker-prefixed check lines and derives a typed verdict.
// The result is advisory — LLM output is non-deterministic and may not follow
// the expected format. The raw text is always preserved for the user to review.
//
// Verdict rules:
//   - Any failed check → VerdictFail
//   - No failures, any skipped → VerdictWarn
//   - All checks passed (at least one) → VerdictPass
//   - No recognised checks → VerdictFail (fail-closed: no evidence ≠ evidence of pass)
func ParseValidationOutput(raw string) ValidationOutput {
	var checks []CheckResult
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "- ")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			continue
		}

		var outcome CheckOutcome
		var rest string
		switch {
		case strings.HasPrefix(trimmed, MarkerPass):
			outcome = CheckPassed
			rest = strings.TrimPrefix(trimmed, MarkerPass)
		case strings.HasPrefix(trimmed, MarkerFail):
			outcome = CheckFailed
			rest = strings.TrimPrefix(trimmed, MarkerFail)
		case strings.HasPrefix(trimmed, MarkerWarn):
			outcome = CheckSkipped
			rest = strings.TrimPrefix(trimmed, MarkerWarn)
		default:
			continue
		}
		checks = append(checks, CheckResult{
			Outcome:   outcome,
			Criterion: strings.TrimSpace(rest),
		})
	}

	// Fail-closed: no marker-prefixed lines means no evidence of success.
	if len(checks) == 0 {
		return ValidationOutput{Checks: nil, Verdict: VerdictFail, Raw: raw}
	}

	verdict := VerdictPass
	for _, c := range checks {
		if c.Outcome == CheckFailed {
			verdict = VerdictFail
			break
		}
		if c.Outcome == CheckSkipped {
			verdict = VerdictWarn
		}
	}

	return ValidationOutput{
		Checks:  checks,
		Verdict: verdict,
		Raw:     raw,
	}
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
func FormatValidationFeedback(report ValidationReport) string {
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
