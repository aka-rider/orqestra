package agent

import (
	"strings"
	"testing"
)

func TestCommitMessagePrompt(t *testing.T) {
	p := CommitMessagePrompt()
	if p == "" {
		t.Fatal("CommitMessagePrompt() returned empty string")
	}
	if !strings.Contains(p, "commit message") {
		t.Error("expected prompt to mention 'commit message'")
	}
	if !strings.Contains(p, "72") {
		t.Error("expected prompt to mention subject line length limit")
	}
}

func TestParseCommitMessage(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "plain message trimmed",
			input: "Add JWT authentication\n",
			want:  "Add JWT authentication",
		},
		{
			name:  "message with body preserved",
			input: "Add JWT authentication\n\nSwitches login endpoint to RS256 tokens.",
			want:  "Add JWT authentication\n\nSwitches login endpoint to RS256 tokens.",
		},
		{
			name:  "strip code fence",
			input: "```\nAdd JWT authentication\n```",
			want:  "Add JWT authentication",
		},
		{
			name:  "strip code fence with language tag",
			input: "```text\nAdd JWT authentication\n```",
			want:  "Add JWT authentication",
		},
		{
			name:    "empty input returns error",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace-only returns error",
			input:   "   \n\t  ",
			wantErr: true,
		},
		{
			name:  "subject line over 72 chars is truncated",
			input: "Refactor the entire authentication module to support multiple identity providers",
			want:  "Refactor the entire authentication module to support multiple identity",
		},
		{
			name:  "subject exactly 72 chars untouched",
			input: strings.Repeat("x", 72),
			want:  strings.Repeat("x", 72),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCommitMessage(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseCommitMessage(%q) = %q, nil; want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCommitMessage(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseCommitMessage(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}
