package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamEventsFrom(t *testing.T) {
	// INV-P4-STREAM: event dispatcher routes each stream event type to the correct Event fields
	cases := []struct {
		name      string
		eventJSON string
		wantText  string
		wantTools []string
	}{
		{
			name:      "content_block_delta writes text",
			eventJSON: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
			wantText:  "hello",
		},
		{
			name:      "assistant text is written",
			eventJSON: `{"type":"assistant","message":{"content":[{"type":"text","text":"thinking..."}]}}`,
			wantText:  "thinking...",
		},
		{
			name:      "assistant tool_use fires OnToolUse",
			eventJSON: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
			wantTools: []string{"Read:main.go"},
		},
		{
			name:      "content_block_start fires OnToolUse",
			eventJSON: `{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}}`,
			wantTools: []string{"Bash:go test ./..."},
		},
		{
			name:      "stream_event wrapping content_block_delta writes text",
			eventJSON: `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"streamed"}}}`,
			wantText:  "streamed",
		},
		{
			name:      "unknown event type is a no-op",
			eventJSON: `{"type":"ping"}`,
			wantText:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var event streamEvent
			if err := json.Unmarshal([]byte(tc.eventJSON), &event); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}

			updates := streamEventsFrom(event)

			var gotText strings.Builder
			var gotTools []string
			for _, u := range updates {
				if u.Text != "" {
					gotText.WriteString(u.Text)
				}
				if u.Tool != "" {
					gotTools = append(gotTools, u.Tool+":"+u.Detail)
				}
			}

			if got := gotText.String(); got != tc.wantText {
				t.Errorf("text: got %q, want %q", got, tc.wantText)
			}
			if len(tc.wantTools) == 0 && len(gotTools) != 0 {
				t.Errorf("unexpected tool calls: %v", gotTools)
			}
			for i, want := range tc.wantTools {
				if i >= len(gotTools) {
					t.Errorf("missing tool call[%d]: want %q", i, want)
					continue
				}
				if gotTools[i] != want {
					t.Errorf("tool[%d]: got %q, want %q", i, gotTools[i], want)
				}
			}
		})
	}
}
