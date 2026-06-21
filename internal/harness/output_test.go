package harness

import (
	"encoding/json"
	"testing"
)

func TestStreamEventToolUse(t *testing.T) {
	// INV-P4-STREAM: tool_use event correctly parsed from stream
	inner := `{"type":"content_block_start","content_block":{"type":"tool_use","name":"Read","input":{"file_path":"go.mod"}}}`
	wrapper := streamEvent{
		Type:  "stream_event",
		Event: json.RawMessage(inner),
	}
	if wrapper.Event == nil {
		t.Fatal("expected non-nil Event field")
	}
	var innerEvt streamEvent
	if err := json.Unmarshal(wrapper.Event, &innerEvt); err != nil {
		t.Fatalf("unmarshal inner event: %v", err)
	}
	name, args := innerEvt.extractToolUse()
	if name != "Read" {
		t.Errorf("tool name = %q, want Read", name)
	}
	if args == nil {
		t.Error("expected non-nil args")
	}
}

func TestStreamEventTextDelta(t *testing.T) {
	// INV-P4-STREAM: text delta correctly extracted from stream
	inner := `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello world"}}`
	wrapper := streamEvent{
		Type:  "stream_event",
		Event: json.RawMessage(inner),
	}
	var innerEvt streamEvent
	if err := json.Unmarshal(wrapper.Event, &innerEvt); err != nil {
		t.Fatalf("unmarshal inner: %v", err)
	}
	if innerEvt.Delta.Text != "hello world" {
		t.Errorf("delta text = %q, want 'hello world'", innerEvt.Delta.Text)
	}
}
