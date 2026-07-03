package agent

import (
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

// Textual fallback prefixes recognised when the LLM emits words instead of
// the ✓/✕/⚠ markers. Matching is case-insensitive on the prefix only; the
// marker forms above remain the primary, preferred contract.
const (
	textPrefixPass   = "pass:"
	textPrefixOK     = "ok:"
	textPrefixFail   = "fail:"
	textPrefixFailed = "failed:"
	textPrefixSkip   = "skip:"
	textPrefixWarn   = "warn:"
)

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
		lower := strings.ToLower(trimmed)
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
		case strings.HasPrefix(lower, textPrefixPass) || strings.HasPrefix(lower, textPrefixOK):
			outcome = CheckPassed
			rest = trimmed[strings.Index(trimmed, ":")+1:]
		case strings.HasPrefix(lower, textPrefixFail) || strings.HasPrefix(lower, textPrefixFailed):
			outcome = CheckFailed
			rest = trimmed[strings.Index(trimmed, ":")+1:]
		case strings.HasPrefix(lower, textPrefixSkip) || strings.HasPrefix(lower, textPrefixWarn):
			outcome = CheckSkipped
			rest = trimmed[strings.Index(trimmed, ":")+1:]
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

// Verdict represents the overall outcome of validation.
type Verdict string

const (
	VerdictPass Verdict = "pass"
	VerdictWarn Verdict = "warn"
	VerdictFail Verdict = "fail"
)
