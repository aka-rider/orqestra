package agent

import (
	"testing"
)

func TestDeriveVerdict(t *testing.T) {
	// INV-P3-VALID: verdict is derived solely from the blocking field of reported issues
	tests := []struct {
		name   string
		issues []Issue
		want   Verdict
	}{
		{"no issues", nil, VerdictPass},
		{"non-blocking only", []Issue{{Blocking: false, Message: "minor"}}, VerdictWarn},
		{"blocking", []Issue{{Blocking: true, Message: "broken"}}, VerdictFail},
		{"blocking overrides non-blocking", []Issue{{Blocking: false}, {Blocking: true}}, VerdictFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveVerdict(tt.issues)
			if got != tt.want {
				t.Errorf("DeriveVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseValidationOutput(t *testing.T) {
	// INV-P4-PARSE: structured marker extraction from raw worker output
	// INV-P3-VALID: verdict follows parsed markers (PASS/FAIL/WARN)
	// INV-ROLE-VALIDATE: the validator agent fails closed — empty/marker-less
	// output derives VerdictFail (no evidence ≠ evidence of pass), on real input.
	tests := []struct {
		name        string
		raw         string
		wantVerdict Verdict
		wantChecks  int
		wantFailed  int
		wantPassed  int
		wantSkipped int
	}{
		{
			name:        "empty output",
			raw:         "",
			wantVerdict: VerdictFail, // fail-closed: no evidence ≠ evidence of pass
		},
		{
			name:        "all passing",
			raw:         MarkerPass + " tests pass\n" + MarkerPass + " lint clean",
			wantVerdict: VerdictPass,
			wantChecks:  2,
			wantPassed:  2,
		},
		{
			name:        "warning only",
			raw:         MarkerWarn + " cannot verify coverage",
			wantVerdict: VerdictWarn,
			wantChecks:  1,
			wantSkipped: 1,
		},
		{
			name:        "has failure",
			raw:         MarkerFail + " tests — exit code 1",
			wantVerdict: VerdictFail,
			wantChecks:  1,
			wantFailed:  1,
		},
		{
			name:        "mixed pass and fail",
			raw:         MarkerPass + " build ok\n" + MarkerFail + " tests fail",
			wantVerdict: VerdictFail,
			wantChecks:  2,
			wantPassed:  1,
			wantFailed:  1,
		},
		{
			name:        "list-prefixed lines",
			raw:         "- " + MarkerPass + " build\n- " + MarkerFail + " test",
			wantVerdict: VerdictFail,
			wantChecks:  2,
			wantPassed:  1,
			wantFailed:  1,
		},
		{
			name:        "non-marker lines ignored",
			raw:         "some preamble\n" + MarkerPass + " check\nsome epilogue",
			wantVerdict: VerdictPass,
			wantChecks:  1,
			wantPassed:  1,
		},
		{
			name:        "LLM ignored format — no markers at all",
			raw:         "I have completed all the tasks successfully.\nEverything looks good.",
			wantVerdict: VerdictFail, // fail-closed: marker-less prose is not evidence of pass
			wantChecks:  0,
		},
		{
			name:        "failure overrides warning",
			raw:         MarkerWarn + " coverage\n" + MarkerFail + " lint",
			wantVerdict: VerdictFail,
			wantChecks:  2,
			wantFailed:  1,
			wantSkipped: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseValidationOutput(tt.raw)
			if result.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", result.Verdict, tt.wantVerdict)
			}
			if len(result.Checks) != tt.wantChecks {
				t.Errorf("len(Checks) = %d, want %d", len(result.Checks), tt.wantChecks)
			}
			if result.Raw != tt.raw {
				t.Error("Raw not preserved")
			}
			var passed, failed, skipped int
			for _, c := range result.Checks {
				switch c.Outcome {
				case CheckPassed:
					passed++
				case CheckFailed:
					failed++
				case CheckSkipped:
					skipped++
				}
			}
			if passed != tt.wantPassed {
				t.Errorf("passed = %d, want %d", passed, tt.wantPassed)
			}
			if failed != tt.wantFailed {
				t.Errorf("failed = %d, want %d", failed, tt.wantFailed)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("skipped = %d, want %d", skipped, tt.wantSkipped)
			}
		})
	}
}

