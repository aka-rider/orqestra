//go:build darwin

package harness

import (
	"testing"
)

func TestExtractStreamResult(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "extracts result from stream-json blob",
			input: `{"type":"system","session_id":"abc"}
{"type":"assistant","message":{"content":[{"type":"text","text":"working..."}]}}
{"type":"result","subtype":"success","result":"Add user login endpoint","usage":{"input_tokens":100,"output_tokens":10}}
`,
			want: "Add user login endpoint",
		},
		{
			name: "returns empty when no result event",
			input: `{"type":"system","session_id":"abc"}
{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}
`,
			want: "",
		},
		{
			name:  "handles empty input",
			input: "",
			want:  "",
		},
		{
			name: "skips non-JSON lines",
			input: `not-json
{"type":"result","result":"Fix nil pointer dereference in parser"}
`,
			want: "Fix nil pointer dereference in parser",
		},
		{
			name: "skips result events with empty result field",
			input: `{"type":"result","result":""}
{"type":"result","result":"Refactor token validation logic"}
`,
			want: "Refactor token validation logic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStreamResult(tt.input)
			if got != tt.want {
				t.Errorf("extractStreamResult() = %q, want %q", got, tt.want)
			}
		})
	}
}
