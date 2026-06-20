package orchestrator

import (
	"testing"
)

func TestLooksLikeReport(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "empty",
			text: "",
			want: false,
		},
		{
			name: "three lines no heading",
			text: "Hello world.\nThis is a report.\nEnd.",
			want: false,
		},
		{
			name: "mostly binary",
			text: "# Plan\n\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f",
			want: false,
		},
		{
			name: "valid plan",
			text: "# Plan\n\n## Goal\nAdd a flag.\n\n## Work Packages\n### 1. Edit main.go\n",
			want: true,
		},
		{
			name: "hash Goal accepted instead of hash Plan",
			text: "# Goal\n\nResearch findings.\n\n## Codebase Facts\nThe repo uses Go modules.\n",
			want: true,
		},
		{
			name: "preamble before heading",
			text: "Here is my report:\n\n# Researcher Report\n\n## Codebase Facts\nGo 1.22\n",
			want: true,
		},
		{
			name: "too short even with heading",
			text: "# X\n",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeReport(tc.text)
			if got != tc.want {
				t.Errorf("looksLikeReport(%q) = %v, want %v", tc.text[:min(len(tc.text), 40)], got, tc.want)
			}
		})
	}
}

