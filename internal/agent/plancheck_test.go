package agent

import (
	"strings"
	"testing"
)

func TestCheckPlanHealth(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		wantWarn []string
	}{
		{
			name: "healthy plan",
			markdown: `# Plan
## Goal
Do something useful with enough words so it goes over 100 characters. We need to make this text reasonably long to pass the stub check without triggering any of the warnings.
## Work Packages
### 1. Do stuff
**Done when:**
- Tests pass.
`,
			wantWarn: nil,
		},
		{
			name: "unclosed fence",
			markdown: `# Plan
## Goal
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
## Work Packages
### 1. Code change
**Done when:**
- It compiles
Here is some code:
` + "```" + `go
func Do() {}
`,
			wantWarn: []string{"unclosed code fence"},
		},
		{
			name: "truncated",
			markdown: `# Plan
## Goal
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
## Work Packages
### 1. Do something
**Done when:**
- It works
And here is a sentence that just stops abruptly mid-sentence wi`,
			wantWarn: []string{"mid-sentence"},
		},
		{
			name: "zero headings",
			markdown: `
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
Just a plain paragraph here. Nothing to see. Let's make it long enough to not fail length check!`,
			wantWarn: []string{"does not start with", "missing '## Work Packages'", "zero headings"},
		},
		{
			name: "suspiciously short",
			markdown: `# Plan
Yup
`,
			wantWarn: []string{"suspiciously short", "missing '## Work Packages'", "mid-sentence"},
		},
		{
			name: "missing plan header",
			markdown: `## Goal
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
## Work Packages
### 1. Do stuff
**Done when:**
- Tests pass
`,
			wantWarn: []string{"does not start with '# Plan'"},
		},
		{
			name: "critic shaped output",
			markdown: `# Critic Report: README.md Refactoring
## Summary Verdict: NEEDS REVISION
Several factual errors were found that need correction before the worker can execute this plan.
We need enough text to not trip the short plan warning so adding more filler here for the test.
`,
			wantWarn: []string{"does not start with", "critic-shaped", "missing '## Work Packages'"},
		},
		{
			name: "missing work packages",
			markdown: `# Plan
## Goal
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
## Context
Some context here to explain the approach without any work packages defined.
`,
			wantWarn: []string{"missing '## Work Packages'"},
		},
		{
			name: "work packages without done-when",
			markdown: `# Plan
## Goal
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
## Work Packages
### 1. Do stuff
Steps: change things around in the codebase to make it better and then ship it.
`,
			wantWarn: []string{"Done when"},
		},
		{
			name: "unresolved critic flags",
			markdown: `# Plan
## Goal
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
## Work Packages
### 1. Do stuff
⚠ CRITIC FLAG: Architect could not verify whether this breaks callers.
**Done when:**
- Tests pass
### 2. More stuff
⚠ CRITIC FLAG: Unclear if backward-compatible.
**Done when:**
- Build succeeds.
`,
			wantWarn: []string{"2 unresolved ⚠ CRITIC FLAG"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			warnings := CheckPlanHealth(tc.markdown)

			if len(warnings) != len(tc.wantWarn) {
				t.Fatalf("expected %d warnings %v, got %d: %v",
					len(tc.wantWarn), tc.wantWarn, len(warnings), warnings)
			}

			for _, want := range tc.wantWarn {
				found := false
				for _, actual := range warnings {
					if strings.Contains(strings.ToLower(actual), strings.ToLower(want)) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected warning containing %q, actual warnings: %v", want, warnings)
				}
			}
		})
	}
}
