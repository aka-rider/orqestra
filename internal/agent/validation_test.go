package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeriveVerdict(t *testing.T) {
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

func TestValidationReport_JSONRoundtrip(t *testing.T) {
	report := ValidationReport{
		SchemaVersion: "1",
		Verdict:       VerdictWarn,
		Summary:       "Minor issues",
		Issues: []Issue{
			{ID: "WARN_1", Blocking: false, Message: "Could be better"},
		},
		Suggestions: []string{"Add more tests"},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ValidationReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Verdict != report.Verdict {
		t.Errorf("verdict: got %q, want %q", decoded.Verdict, report.Verdict)
	}
	if len(decoded.Issues) != 1 {
		t.Errorf("issues: got %d, want 1", len(decoded.Issues))
	}
}

func TestFormatValidationFeedback(t *testing.T) {
	report := ValidationReport{
		Verdict: VerdictFail,
		Summary: "Plan is incomplete",
		Issues: []Issue{
			{ID: "MISSING", Blocking: true, Message: "no tests"},
		},
		Suggestions: []string{"Add test steps"},
	}
	feedback := FormatValidationFeedback(report)
	if !strings.Contains(feedback, "MISSING") {
		t.Error("feedback should contain issue ID")
	}
	if !strings.Contains(feedback, "Add test steps") {
		t.Error("feedback should contain suggestions")
	}
}

func TestParseValidationOutput(t *testing.T) {
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
			wantVerdict: VerdictPass,
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
			wantVerdict: VerdictPass,
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

func TestParseValidationOutput_CriterionText(t *testing.T) {
	raw := MarkerPass + " tests pass — exit 0\n" + MarkerFail + " lint errors — 3 issues found"
	result := ParseValidationOutput(raw)
	if len(result.Checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(result.Checks))
	}
	if result.Checks[0].Criterion != "tests pass — exit 0" {
		t.Errorf("check[0].Criterion = %q", result.Checks[0].Criterion)
	}
	if result.Checks[1].Criterion != "lint errors — 3 issues found" {
		t.Errorf("check[1].Criterion = %q", result.Checks[1].Criterion)
	}
}
