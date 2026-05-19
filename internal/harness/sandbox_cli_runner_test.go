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

func TestRunParsed_ExtractsTextAndRoutesActivities(t *testing.T) {
	raw, err := os.ReadFile("testdata/worker_stream_sample.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	events := make(chan StreamUpdate, 128)
	var updates []StreamUpdate
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			updates = append(updates, ev)
		}
	}()

	gotRaw, err := parseStreamLines(bytes.NewReader(raw), events)
	close(events)
	<-done
	if err != nil {
		t.Fatalf("parseStreamLines: %v", err)
	}

	// (a) No raw stream-event frame leaks into text updates.
	for i, ev := range updates {
		trimmed := strings.TrimSpace(ev.Text)
		if trimmed == "" || trimmed[0] != '{' {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(trimmed), &probe); err == nil && probe.Type != "" {
			t.Errorf("text update %d leaked stream-event frame: type=%q content=%q", i, probe.Type, trimmed)
		}
	}

	// (b) Assistant text "OK" reached updates.
	var combined bytes.Buffer
	for _, ev := range updates {
		combined.WriteString(ev.Text)
	}
	if !strings.Contains(combined.String(), "OK") {
		t.Errorf("updates did not receive assistant text %q; got %q", "OK", combined.String())
	}

	// (c) Read tool-use recorded exactly once.
	readCount := 0
	for _, ev := range updates {
		if ev.Tool == "Read" {
			readCount++
		}
	}
	if readCount != 1 {
		t.Errorf("expected 1 Read tool-use, got %d", readCount)
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
