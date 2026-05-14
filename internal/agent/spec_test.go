package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSpecification_JSONRoundtrip(t *testing.T) {
	spec := Specification{
		SchemaVersion: "1",
		ID:            "test-001",
		Title:         "Test Spec",
		Goal:          "Build a thing",
		Context:       "Some context",
		Steps:         []string{"Step 1", "Step 2"},
		Acceptance:    []string{"It works"},
		Scope: &Scope{
			IncludeGlobs: []string{"src/**"},
			ExcludeGlobs: []string{"vendor/**"},
		},
		Constraints: []string{"No external deps"},
		Assumptions: []string{"Go installed"},
		Risks:       []string{"Might be slow"},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Specification
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Goal != spec.Goal {
		t.Errorf("goal: got %q, want %q", decoded.Goal, spec.Goal)
	}
	if len(decoded.Steps) != len(spec.Steps) {
		t.Errorf("steps: got %d, want %d", len(decoded.Steps), len(spec.Steps))
	}
	if decoded.Scope == nil {
		t.Fatal("scope should not be nil")
	}
}

func TestBuildExecutionPrompt(t *testing.T) {
	spec := Specification{
		Goal:       "Build an API",
		Steps:      []string{"Create server", "Add routes"},
		Acceptance: []string{"Server starts"},
	}
	prompt := BuildExecutionPrompt(spec)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "Build an API") {
		t.Error("prompt should contain the goal")
	}
	if !strings.Contains(prompt, "1. Create server") {
		t.Error("prompt should contain numbered steps")
	}
	if !strings.Contains(prompt, "Server starts") {
		t.Error("prompt should contain acceptance criteria")
	}
}

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
