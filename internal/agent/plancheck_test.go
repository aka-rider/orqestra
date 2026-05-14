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
			markdown: `
# Plan
## Goal
Do something useful with enough words so it goes over 100 characters. We need to make this text reasonably long to pass the stub check without triggering any of the warnings.
## Work Packages
Yep, this has a heading. All good to go!
`,
			wantWarn: nil,
		},
		{
			name: "unclosed fence",
			markdown: `
# Plan
## Goal
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
Here is some code:
` + "```" + `go
func Do() {}
`,
			wantWarn: []string{"unclosed code fence"},
		},
		{
			name: "truncated",
			markdown: `
# Plan
## Goal
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
And here is a sentence that just stops abruptly mid-sentence wi`,
			wantWarn: []string{"mid-sentence"},
		},
		{
			name: "zero headings",
			markdown: `
We need to make this text reasonably long to pass the stub check without triggering any false warnings.
Just a plain paragraph here. Nothing to see. Let's make it long enough to not fail length check!`,
			wantWarn: []string{"zero headings"},
		},
		{
			name: "suspiciously short",
			markdown: `
# Plan
Yup
`,
			wantWarn: []string{"suspiciously short"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			warnings := CheckPlanHealth(tc.markdown)

			if len(warnings) == 0 && tc.wantWarn != nil {
				t.Fatalf("expected warnings containing %v, got none", tc.wantWarn)
			}
			if len(warnings) > 0 && tc.wantWarn == nil {
				t.Fatalf("expected no warnings, got %v", warnings)
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
