package agent

import (
	"fmt"
	"strings"
)

// ResearcherPrompt wraps the user task for the researcher stage, reinforcing the
// fact-report role at the point of attention (the last thing the model reads).
func ResearcherPrompt(userPrompt string) string {
	return fmt.Sprintf("<user_request>\n%s\n</user_request>\n\nResearch the codebase and produce the FACT REPORT defined in your instructions for the request above. Report what exists; do not propose, plan, or implement changes.",
		userPrompt)
}

// ArchitectPrompt builds the initial planning prompt from research facts.
func ArchitectPrompt(userPrompt, researchFacts string) string {
	return fmt.Sprintf("<user_request>\n%s\n</user_request>\n\n<codebase_research>\n%s\n</codebase_research>\n\nUsing the codebase research above, produce THE PLAN for the user's request — a spec a separate Worker will execute. You design it; you do not write code.",
		userPrompt, researchFacts)
}

// ArchitectRevisionPrompt builds a cold-start revision prompt (no session to resume).
func ArchitectRevisionPrompt(previousPlan, comments string) string {
	return fmt.Sprintf("Revise this plan based on the reviewer's comments:\n\n## Current Plan\n\n%s\n\n## Reviewer Comments\n\n%s", previousPlan, comments)
}

// ContinuePrompt builds the continue-session prompt for a reviewer comment.
func ContinuePrompt(currentPlan, comment string) string {
	return fmt.Sprintf(`The current implementation plan is below. The reviewer sent a message.

<current_plan>
%s
</current_plan>

<reviewer_message>
%s
</reviewer_message>

If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
If the reviewer requests changes, revise the plan.`, currentPlan, comment)
}

// ContinueWithDiffPrompt builds the continue-session prompt when the reviewer
// edited the plan directly (Ctrl+E). diff shows what changed; comment is optional.
func ContinueWithDiffPrompt(currentPlan, diff, comment string) string {
	return fmt.Sprintf(`The reviewer edited the plan directly. Here are their changes:

<plan_changes>
%s
</plan_changes>

<current_plan>
%s
</current_plan>

<reviewer_message>
%s
</reviewer_message>

Focus on the reviewer's edits first. If the reviewer asks a question, answer it.
If the reviewer requests further changes, revise the plan.`, diff, currentPlan, comment)
}

// CriticContinuePrompt builds the continue-session prompt for architect to respond
// to a critic report.
func CriticContinuePrompt(currentPlan, criticReport string) string {
	return fmt.Sprintf(`A Plan Critic agent reviewed your plan and produced the report below.
The Critic had read-only tool access and spot-checked your claims
against the codebase.

<critic_report>
%s
</critic_report>

<current_plan>
%s
</current_plan>

Review every finding. For each:
- If you can verify the issue is real and you know the fix: apply it to
  the plan.
- If you cannot determine whether the issue is valid, or the fix requires
  a judgment call you cannot make: surface it inline in the relevant
  section of the plan, clearly marked with ⚠ CRITIC FLAG, so the human
  reviewer can decide.

Do NOT discard findings silently. Every finding must be either fixed or
flagged.

Output the COMPLETE updated plan starting with "# Plan". Even if you
judge all findings to be non-issues, re-output the plan with inline
notes explaining why.`, criticReport, currentPlan)
}

// CriticReviewPrompt builds the prompt for the critic to review a plan.
func CriticReviewPrompt(userPrompt, planMarkdown string) string {
	return fmt.Sprintf("<user_request>\n%s\n</user_request>\n\n<implementation_plan>\n%s\n</implementation_plan>\n\nReview the implementation plan above against the actual codebase. Produce a Critic Report.",
		userPrompt, planMarkdown)
}

// CheckPromptIntegrity verifies that originalPrompt appears verbatim in assembled.
// If it does not, the prompt is prepended as <original_prompt>...</original_prompt>
// and the function returns (fixed, true). A tripped canary signals a template bug
// or transfer-chain corruption — the run continues degraded-but-truthful.
func CheckPromptIntegrity(assembled, originalPrompt string) (string, bool) {
	if originalPrompt == "" || strings.Contains(assembled, originalPrompt) {
		return assembled, false
	}
	fixed := "<original_prompt>\n" + originalPrompt + "\n</original_prompt>\n\n" + assembled
	return fixed, true
}
