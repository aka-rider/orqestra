package harness

import (
	"strings"
	"testing"
)

func TestResultSubtypeError(t *testing.T) {
	// INV-P4-STREAM: error_max_turns subtype → isError=true regardless of is_error field
	tests := []struct {
		name    string
		jsonl   string
		wantErr bool
	}{
		{
			name:    "error_max_turns with is_error false",
			jsonl:   `{"type":"result","subtype":"error_max_turns","is_error":false,"result":"stopped"}`,
			wantErr: true,
		},
		{
			name:    "error_max_turns with is_error true",
			jsonl:   `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"stopped"}`,
			wantErr: true,
		},
		{
			name:    "success subtype",
			jsonl:   `{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
			wantErr: false,
		},
		{
			name:    "no subtype, is_error false",
			jsonl:   `{"type":"result","is_error":false,"result":"done"}`,
			wantErr: false,
		},
		{
			name:    "no subtype, is_error true",
			jsonl:   `{"type":"result","is_error":true,"result":"oops"}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, isErr, _, _, _, err := parseStream(strings.NewReader(tt.jsonl), nil)
			if err != nil {
				t.Fatalf("parseStream error: %v", err)
			}
			if isErr != tt.wantErr {
				t.Errorf("isError = %v, want %v", isErr, tt.wantErr)
			}
		})
	}
}

func TestEventExtensions(t *testing.T) {
	// INV-P4-STREAM: ExitWorktree Args, tool_result IsError, and EventSessionDone from real JSONL shapes
	// (b) user tool_result with is_error:true → EventToolResult{IsError:true}
	// (c) result line → exactly one EventSessionDone
	jsonl := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ExitWorktree","input":{"action":"keep"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"err"}]}]}}
{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s1"}`

	var toolUseEvs []Event
	var toolResultEvs []Event
	var sessionDoneCount int

	events := make(chan Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			switch ev.Kind {
			case EventToolUse:
				toolUseEvs = append(toolUseEvs, ev)
			case EventToolResult:
				toolResultEvs = append(toolResultEvs, ev)
			case EventSessionDone:
				sessionDoneCount++
			}
		}
	}()

	_, _, _, _, _, err := parseStream(strings.NewReader(jsonl), events)
	close(events)
	<-done

	if err != nil {
		t.Fatalf("parseStream error: %v", err)
	}

	// (a) ExitWorktree tool_use should have Args set
	if len(toolUseEvs) == 0 {
		t.Fatal("expected at least one EventToolUse")
	}
	ev := toolUseEvs[0]
	if ev.Tool != "ExitWorktree" {
		t.Errorf("Tool = %q, want ExitWorktree", ev.Tool)
	}
	if ev.Args != `{"action":"keep"}` {
		t.Errorf("Args = %q, want {\"action\":\"keep\"}", ev.Args)
	}

	// (b) tool_result with is_error:true
	if len(toolResultEvs) == 0 {
		t.Fatal("expected at least one EventToolResult")
	}
	if !toolResultEvs[0].IsError {
		t.Error("EventToolResult.IsError = false, want true")
	}

	// (c) exactly one EventSessionDone
	if sessionDoneCount != 1 {
		t.Errorf("EventSessionDone count = %d, want 1", sessionDoneCount)
	}
}
