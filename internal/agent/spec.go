package agent

import (
	"errors"
	"fmt"
	"strings"
)

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
- %s <criterion> — <evidence: command exited 0 / file content verified>
- %s <criterion> — <evidence: command output showing failure>
- %s <criterion> — cannot verify (explain why)

Do not claim a command passed unless you observed exit code 0.
If failures remain after retries, report them plainly. Do not hide or minimize.`, retryBudget, MarkerPass, MarkerFail, MarkerWarn)
}

// ParseCommitMessage extracts a clean git commit message from raw LLM output.
// It strips surrounding code fences, trims whitespace, and truncates the
// subject line to 72 characters. Returns an error if the result is empty.
func ParseCommitMessage(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("commit message: empty response")
	}

	// Strip a single outermost markdown code fence if present.
	if strings.HasPrefix(s, "```") {
		nl := strings.Index(s, "\n")
		if nl != -1 {
			inner := s[nl+1:]
			if close := strings.LastIndex(inner, "```"); close != -1 {
				s = strings.TrimSpace(inner[:close])
			}
		}
	}

	if s == "" {
		return "", errors.New("commit message: empty after stripping code fence")
	}

	// Truncate subject line to 72 characters, trimming at a word boundary.
	lines := strings.SplitN(s, "\n", 2)
	if len(lines[0]) > 72 {
		subject := lines[0][:72]
		if i := strings.LastIndex(subject, " "); i > 0 {
			subject = subject[:i]
		}
		lines[0] = subject
	}
	return strings.Join(lines, "\n"), nil
}
