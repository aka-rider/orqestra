//go:build fuzz

package agent

import (
	"strings"
	"testing"
)

func FuzzParseValidationOutput(f *testing.F) {
	f.Add("PASS: did the thing\n")
	f.Add("FAIL: wrong\n")
	f.Add("")
	f.Add("PASS: a\nFAIL: b\n")
	f.Add("Looks good to me")

	f.Fuzz(func(t *testing.T, input string) {
		result := ParseValidationOutput(input)

		if result.Raw != input {
			t.Fatalf("Raw not preserved: got %q, want %q", result.Raw, input)
		}
		if strings.TrimSpace(input) == "" && result.Verdict != VerdictFail {
			t.Fatalf("empty input must yield VerdictFail (fail-closed), got %q", result.Verdict)
		}
	})
}

func FuzzParseCommitMessage(f *testing.F) {
	f.Add("feat: add feature")
	f.Add("```\nfeat: message\n```")
	f.Add("   \t\n")

	f.Fuzz(func(t *testing.T, input string) {
		result, err := ParseCommitMessage(input)
		if err != nil {
			return
		}
		// Only the subject line (before first newline) is bounded to 72 chars.
		subject := strings.SplitN(result, "\n", 2)[0]
		if len(subject) > 72 {
			t.Fatalf("subject line exceeds 72 chars (%d): %q", len(subject), subject)
		}
		if strings.TrimSpace(result) == "" {
			t.Fatalf("non-error result must not be whitespace-only")
		}
	})
}
