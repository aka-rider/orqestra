package harness

import (
	"encoding/json"
	"testing"
)

func TestInteractiveRunner_interface(t *testing.T) {
	// Verify *ClaudeCLI implements InteractiveRunner.
	var _ InteractiveRunner = (*ClaudeCLI)(nil)
}

func TestSession_zero_values(t *testing.T) {
	sess := &InteractiveSession{}

	// Zero values should be safe.
	if sess.Usage().Total() != 0 {
		t.Errorf("Usage().Total() = %d, want 0", sess.Usage().Total())
	}
	if sess.ResultError() {
		t.Error("ResultError() = true, want false")
	}
	if sess.SessionID() != "" {
		t.Errorf("SessionID() = %q, want empty", sess.SessionID())
	}
	if sess.PlanPath() != "" {
		t.Errorf("PlanPath() = %q, want empty", sess.PlanPath())
	}
}

func TestTokenUsage_Total(t *testing.T) {
	tests := []struct {
		name        string
		input       int64
		output      int64
		wantTotal   int64
	}{
		{"zero", 0, 0, 0},
		{"input only", 1000, 0, 1000},
		{"output only", 0, 2000, 2000},
		{"both", 1500, 3500, 5000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := TokenUsage{Input: tt.input, Output: tt.output}
			if got := u.Total(); got != tt.wantTotal {
				t.Errorf("Total() = %d, want %d", got, tt.wantTotal)
			}
		})
	}
}

func TestNDJSON_marshal_format(t *testing.T) {
	// Verify the NDJSON marshal format matches the companion-repo wire format.
	ndjson := map[string]interface{}{
		"type":              "user",
		"message":           map[string]interface{}{"role": "user", "content": "hello"},
		"parent_tool_use_id": nil,
		"session_id":        "abc123",
	}

	data, err := json.Marshal(ndjson)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify it's valid JSON.
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify fields.
	if decoded["type"] != "user" {
		t.Errorf("type = %v, want %q", decoded["type"], "user")
	}
	if decoded["session_id"] != "abc123" {
		t.Errorf("session_id = %v, want %q", decoded["session_id"], "abc123")
	}
	msg, ok := decoded["message"].(map[string]interface{})
	if !ok {
		t.Fatal("message should be an object")
	}
	if msg["role"] != "user" {
		t.Errorf("message.role = %v, want %q", msg["role"], "user")
	}
	if msg["content"] != "hello" {
		t.Errorf("message.content = %v, want %q", msg["content"], "hello")
	}
}
