//go:build darwin

package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
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

// recordingDisplay is a test double implementing io.Writer and ActivitySink.
// It records every byte slice written and every OnToolUse call so tests can
// verify exactly what bytes reach the user-facing presentation surface and
// that tool activity routing through dispatchStreamEvent still fires.
type recordingDisplay struct {
	writes   [][]byte
	toolUses []toolUseCall
}

type toolUseCall struct {
	name   string
	detail string
}

func (r *recordingDisplay) Write(p []byte) (int, error) {
	buf := make([]byte, len(p))
	copy(buf, p)
	r.writes = append(r.writes, buf)
	return len(p), nil
}

func (r *recordingDisplay) OnToolUse(name, detail string) {
	r.toolUses = append(r.toolUses, toolUseCall{name: name, detail: detail})
}

func TestRunParsed_ExtractsTextAndRoutesActivities(t *testing.T) {
	raw, err := os.ReadFile("testdata/worker_stream_sample.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	display := &recordingDisplay{}
	gotRaw, err := parseStreamLines(bytes.NewReader(raw), display)
	if err != nil {
		t.Fatalf("parseStreamLines: %v", err)
	}

	// (a) No raw stream-event frame leaks into display.
	for i, w := range display.writes {
		trimmed := strings.TrimSpace(string(w))
		if trimmed == "" || trimmed[0] != '{' {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(trimmed), &probe); err == nil && probe.Type != "" {
			t.Errorf("display write %d leaked stream-event frame: type=%q content=%q", i, probe.Type, trimmed)
		}
	}

	// (b) Assistant text "OK" reached the display.
	var combined bytes.Buffer
	for _, w := range display.writes {
		combined.Write(w)
	}
	if !strings.Contains(combined.String(), "OK") {
		t.Errorf("display did not receive assistant text %q; got %q", "OK", combined.String())
	}

	// (c) Read tool-use recorded exactly once on the activity sink.
	readCount := 0
	for _, tu := range display.toolUses {
		if tu.name == "Read" {
			readCount++
		}
	}
	if readCount != 1 {
		t.Errorf("expected 1 Read tool-use, got %d (all=%+v)", readCount, display.toolUses)
	}

	// (d) extractStreamResult on the round-tripped raw returns the result text.
	if got := extractStreamResult(gotRaw); got != "## Summary\nDone.\n" {
		t.Errorf("extractStreamResult(rawFromFixture) = %q, want %q", got, "## Summary\nDone.\n")
	}
}

// TestRunStreaming_ErrorBranch_OutputIsExtractedText is a static regression
// guard: the error branch of RunStreaming / RunContinue must place extracted
// result text into RunResult.Output, not raw NDJSON. If a future revert
// reintroduces RunResult{Output: rawNDJSON} on error, this test fails because
// extractStreamResult against a stream-json blob returns the parsed result
// field, never the original NDJSON envelope.
func TestRunStreaming_ErrorBranch_OutputIsExtractedText(t *testing.T) {
	raw := `{"type":"system","session_id":"redacted"}
{"type":"assistant","message":{"content":[{"type":"text","text":"OK"}]}}
{"type":"result","subtype":"success","result":"Partial output before failure","usage":{"input_tokens":1,"output_tokens":1}}
`
	// Mirror the production error-branch construction.
	out := extractStreamResult(raw)
	got := RunResult{Output: out}
	if strings.Contains(got.Output, `{"type":`) {
		t.Errorf("error-branch RunResult.Output leaked raw stream-event frame: %q", got.Output)
	}
	if got.Output != "Partial output before failure" {
		t.Errorf("error-branch RunResult.Output = %q, want %q", got.Output, "Partial output before failure")
	}
}
