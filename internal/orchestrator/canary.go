package orchestrator

import (
	"fmt"
	"regexp"
	"strings"
)

// canaryThreshold is the maximum tolerated failed-weight fraction of a role's
// signal panel before its output is rejected. Vetoes fail regardless of fraction.
const canaryThreshold = 0.30

// canarySignal is one dimension of role adherence. fail reports whether the
// signal is violated for a given output (lower = lowercased markdown, raw = the
// original text). A veto signal that fails rejects the whole output regardless of
// the aggregate weighted fraction — reserved for unambiguous role breaks (e.g. a
// researcher emitting file-by-file work packages, a critic offering to implement).
type canarySignal struct {
	name   string
	weight float64
	veto   bool
	fail   func(lower, raw string) bool
}

// canarySpectrum scores an agent's output across a weighted panel of signals.
// check returns an error when the failed-weight fraction exceeds threshold OR any
// veto signal fires. Its signature matches the qualityCheck func(string) error
// consumed by extractPlan/extractWithFallback, so it is a drop-in quality gate.
type canarySpectrum struct {
	role      string
	threshold float64
	signals   []canarySignal
}

// score reports the failed-weight fraction plus the names of failed and vetoed
// signals. Signals are evaluated in declaration order (deterministic).
func (s canarySpectrum) score(md string) (failedFrac float64, failed, vetoed []string) {
	lower := strings.ToLower(md)
	var total, bad float64
	for _, sig := range s.signals {
		total += sig.weight
		if sig.fail(lower, md) {
			bad += sig.weight
			failed = append(failed, sig.name)
			if sig.veto {
				vetoed = append(vetoed, sig.name)
			}
		}
	}
	if total == 0 {
		return 0, failed, vetoed
	}
	return bad / total, failed, vetoed
}

// check is the runtime quality gate. A vetoed or over-threshold output returns an
// error naming the offending signals; the caller's retry/fallback path then fires,
// and on exhaustion the pipeline fails closed.
func (s canarySpectrum) check(md string) error {
	frac, failed, vetoed := s.score(md)
	if len(vetoed) > 0 {
		return fmt.Errorf("%s output tripped role-break veto (%s)", s.role, strings.Join(vetoed, ", "))
	}
	if frac > s.threshold {
		return fmt.Errorf("%s output failed %.0f%% of role-adherence signals (limit %.0f%%): %s",
			s.role, frac*100, s.threshold*100, strings.Join(failed, ", "))
	}
	return nil
}

// --- signal builders ---------------------------------------------------------

// requirePresent fails when needle is missing (a required section/marker).
func requirePresent(needle string) func(lower, raw string) bool {
	n := strings.ToLower(needle)
	return func(lower, _ string) bool { return !strings.Contains(lower, n) }
}

// requireAbsent fails when needle appears (a forbidden marker).
func requireAbsent(needle string) func(lower, raw string) bool {
	n := strings.ToLower(needle)
	return func(lower, _ string) bool { return strings.Contains(lower, n) }
}

// requireStartsWith fails when the trimmed output does not begin with prefix
// (case-insensitive). Preamble is tolerated by qualityPassOrTrim upstream, which
// retries this check against the text from the first heading onward.
func requireStartsWith(prefix string) func(lower, raw string) bool {
	p := strings.ToLower(prefix)
	return func(_, raw string) bool {
		return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), p)
	}
}

// forbidMatch fails when re matches the raw output.
func forbidMatch(re *regexp.Regexp) func(lower, raw string) bool {
	return func(_, raw string) bool { return re.MatchString(raw) }
}

// --- shared patterns ---------------------------------------------------------

var (
	// "I'll implement", "let me implement", "now I'll implement", "I'll start
	// implementing", "ready to implement" — the architect/researcher drift tell.
	reImplementVerb = regexp.MustCompile(`(?i)\b(?:i'?ll|i will|let me|let's|now i'?ll|ready to|start)\s+(?:start\s+|now\s+)?implement`)
	// A researcher/critic emitting file-by-file work packages ("### File 1: …").
	reFileWorkPackage = regexp.MustCompile(`(?im)^#{2,4}\s+file\s+\d`)
	// A researcher emitting a "## Plan" section (H2) — the core "jumped to plan" break.
	reH2Plan = regexp.MustCompile(`(?im)^##\s+plan\b`)
	// A critic re-planning by emitting a "# Plan" (H1) of its own.
	reH1Plan = regexp.MustCompile(`(?im)^#\s+plan\b`)
	// Plain-text question to the user instead of finishing the report.
	rePlainQuestion = regexp.MustCompile(`(?i)\b(pick one|want me to|shall i|should i\b|which (?:option|one) (?:would|do) you|say the word)\b`)
)

// --- per-role spectra --------------------------------------------------------
//
// Each role's deliverable is scored against required sections (requirePresent /
// requireStartsWith) and forbidden role-breaks (requireAbsent / forbidMatch, some
// veto). Tuned against the recorded failure corpus in .orqestra/sessions (see
// canary_test.go): the run 2026-06-19-172044 researcher "## Plan" output and the
// critic "Pick one" output must both fail.

var researchSpectrum = canarySpectrum{
	role:      "researcher",
	threshold: canaryThreshold,
	signals: []canarySignal{
		// Note: ## User Task is injected deterministically by the orchestrator
		// (ensureUserTask) after this gate runs, so it is not scored here.
		{name: "has-codebase-facts", weight: 1, fail: requirePresent("## Codebase Facts")},
		{name: "has-constraints", weight: 1, fail: requirePresent("## Constraints Discovered")},
		{name: "has-gotchas", weight: 1, fail: requirePresent("## Gotchas")},
		{name: "no-plan-section", weight: 2, veto: true, fail: forbidMatch(reH2Plan)},
		{name: "no-work-packages", weight: 2, veto: true, fail: forbidMatch(reFileWorkPackage)},
		{name: "no-implementation-plan", weight: 2, veto: true, fail: requireAbsent("## Implementation Plan")},
		{name: "no-implement-verb", weight: 2, veto: true, fail: forbidMatch(reImplementVerb)},
		{name: "no-plain-question", weight: 1, fail: forbidMatch(rePlainQuestion)},
	},
}

var architectSpectrum = canarySpectrum{
	role:      "architect",
	threshold: canaryThreshold,
	signals: []canarySignal{
		{name: "starts-with-plan", weight: 2, fail: requireStartsWith("# Plan")},
		{name: "has-work-packages", weight: 1, fail: requirePresent("## Work Packages")},
		{name: "has-verification", weight: 1, fail: requirePresent("## Verification")},
		{name: "no-implement-verb", weight: 2, veto: true, fail: forbidMatch(reImplementVerb)},
		{name: "no-plain-question", weight: 1, fail: forbidMatch(rePlainQuestion)},
	},
}

var criticSpectrum = canarySpectrum{
	role:      "critic",
	threshold: canaryThreshold,
	signals: []canarySignal{
		{name: "has-critic-report", weight: 2, fail: requirePresent("## Critic Report")},
		{name: "has-blockers-or-none", weight: 1, fail: func(lower, _ string) bool {
			return !strings.Contains(lower, "### blockers found") &&
				!strings.Contains(lower, "zero blocker") &&
				!strings.Contains(lower, "no blocker")
		}},
		{name: "has-summary", weight: 1, fail: requirePresent("### Summary")},
		{name: "no-implement-verb", weight: 2, veto: true, fail: forbidMatch(reImplementVerb)},
		{name: "no-replan", weight: 2, veto: true, fail: forbidMatch(reH1Plan)},
		{name: "no-plain-question", weight: 1, fail: forbidMatch(rePlainQuestion)},
	},
}
