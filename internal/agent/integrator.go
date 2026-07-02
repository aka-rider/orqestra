package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// integratorGiveUpPattern matches the give-up sentinel with tolerance for
// drift a 30B-class model may introduce: case, a leading markdown bullet
// (`- `), bold markers (`**...**`) around the token and/or colon, and a
// missing colon before the reason.
var integratorGiveUpPattern = regexp.MustCompile(`(?i)^[-*\s]*INTEGRATOR-GIVE-UP\*{0,2}\s*:?\s*\*{0,2}\s*(.*)$`)

// IntegratorCommitMessagePrompt builds the prompt for the Integrator to produce
// a semantic commit message from the worker's diff against the base branch.
func IntegratorCommitMessagePrompt(diff, planGoal string) string {
	var sb strings.Builder
	sb.WriteString("Produce a single git commit message for the following change.\n\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Subject ≤ 72 characters, imperative mood, no trailing period.\n")
	sb.WriteString("- Describe from the product's perspective what changed, not what functions were added.\n")
	sb.WriteString("  GOOD: \"Add retry with exponential backoff to the database connector\"\n")
	sb.WriteString("  BAD:  \"retry_connect, sleep_for functions added to database.py\"\n")
	sb.WriteString("- Never enumerate file names or function names in the subject line.\n")
	sb.WriteString("- Omit words like \"Orqestra\", \"automated\", \"AI\", or \"generated\".\n")
	sb.WriteString("- Output ONLY the commit message text. No explanation, no markdown, no code fences.\n")
	if planGoal != "" {
		sb.WriteString("\n## Plan goal\n")
		sb.WriteString(planGoal)
		sb.WriteString("\n")
	}
	if diff != "" {
		sb.WriteString("\n## Diff\n```diff\n")
		sb.WriteString(diff)
		sb.WriteString("\n```\n")
	}
	return sb.String()
}

// IntegratorConflictPrompt builds the prompt for the Integrator to attempt
// conflict resolution. Files are sorted for determinism.
func IntegratorConflictPrompt(files []string) string {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	fileList := "- " + strings.Join(sorted, "\n- ")
	return fmt.Sprintf(`Attempt to resolve the merge conflicts in the following files:
%s

Rules:
- Read each file. Understand what both sides contributed. Resolve ONLY if you are
  completely certain the resolution preserves ALL intent from BOTH sides.
- The MOMENT you are unsure, the change is non-trivial, or resolving would drop
  either side's work: output exactly "INTEGRATOR-GIVE-UP: <reason>" and change nothing.
- Do NOT force a resolution you are uncertain about. Giving up is the correct, expected outcome.
- Do NOT edit any file outside the conflict list above.
- Do NOT run any shell commands or git commands.`, fileList)
}

// ParseIntegratorGiveUp checks whether raw agent output contains the give-up
// sentinel. Returns (reason, true) if the agent gave up, ("", false) otherwise.
// Matching is tolerant of the drift a 30B-class model may introduce: case,
// a leading list marker, markdown bold around the sentinel or colon, and a
// missing colon before the reason.
func ParseIntegratorGiveUp(raw string) (reason string, gaveUp bool) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := integratorGiveUpPattern.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1]), true
		}
	}
	return "", false
}
