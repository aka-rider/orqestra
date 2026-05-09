package harness

import (
	"encoding/json"
	"testing"
)

func TestToolDetail(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		args     string
		expected string
	}{
		{
			name:     "Read extracts file_path",
			tool:     "Read",
			args:     `{"file_path":"/Users/dev/go.mod","start_line":1}`,
			expected: "/Users/dev/go.mod",
		},
		{
			name:     "Write extracts file_path",
			tool:     "Write",
			args:     `{"file_path":"internal/tui/model.go","content":"..."}`,
			expected: "internal/tui/model.go",
		},
		{
			name:     "MultiEdit extracts file_path",
			tool:     "MultiEdit",
			args:     `{"file_path":"main.go","edits":[]}`,
			expected: "main.go",
		},
		{
			name:     "Bash extracts short command",
			tool:     "Bash",
			args:     `{"command":"ls -la"}`,
			expected: "ls -la",
		},
		{
			name:     "Bash truncates long command",
			tool:     "Bash",
			args:     `{"command":"find /Users/dev/project -name '*.go' -exec grep -l 'something very specific' {} + | sort | uniq -c | sort -rn"}`,
			expected: "find /Users/dev/project -name '*.go' -exec grep -l 'somethin…",
		},
		{
			name:     "Grep extracts pattern",
			tool:     "Grep",
			args:     `{"pattern":"StreamBuffer"}`,
			expected: "StreamBuffer",
		},
		{
			name:     "Glob extracts pattern",
			tool:     "Glob",
			args:     `{"pattern":"**/*.go"}`,
			expected: "**/*.go",
		},
		{
			name:     "MCP tool with 3 parts",
			tool:     "mcp__context7__resolve_library",
			args:     `{}`,
			expected: "context7/resolve_library",
		},
		{
			name:     "Unknown tool returns empty",
			tool:     "SomeNewTool",
			args:     `{"foo":"bar"}`,
			expected: "",
		},
		{
			name:     "Empty args returns empty",
			tool:     "Read",
			args:     ``,
			expected: "",
		},
		{
			name:     "Invalid JSON returns empty",
			tool:     "Read",
			args:     `not json`,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var args json.RawMessage
			if tt.args != "" {
				args = json.RawMessage(tt.args)
			}
			got := ToolDetail(tt.tool, args)
			if got != tt.expected {
				t.Errorf("ToolDetail(%q, %s) = %q, want %q", tt.tool, tt.args, got, tt.expected)
			}
		})
	}
}

func TestExtractToolUse(t *testing.T) {
	tests := []struct {
		name         string
		contentBlock string
		wantName     string
		wantArgs     bool
	}{
		{
			name:         "tool_use content block",
			contentBlock: `{"type":"tool_use","name":"Read","input":{"file_path":"go.mod"}}`,
			wantName:     "Read",
			wantArgs:     true,
		},
		{
			name:         "text content block ignored",
			contentBlock: `{"type":"text","text":"hello"}`,
			wantName:     "",
			wantArgs:     false,
		},
		{
			name:         "nil content block",
			contentBlock: "",
			wantName:     "",
			wantArgs:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &streamEvent{Type: "content_block_start"}
			if tt.contentBlock != "" {
				e.ContentBlock = json.RawMessage(tt.contentBlock)
			}
			name, args := e.extractToolUse()
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if tt.wantArgs && args == nil {
				t.Error("expected args, got nil")
			}
			if !tt.wantArgs && args != nil {
				t.Errorf("expected nil args, got %s", args)
			}
		})
	}
}

func TestStreamEventToolUse(t *testing.T) {
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

func TestAssistantToolUseFallback(t *testing.T) {
	msg := `{"content":[{"type":"text","text":"analyzing..."},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}`
	e := &streamEvent{
		Type:    "assistant",
		Message: json.RawMessage(msg),
	}
	tools := e.extractAssistantToolUses()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool use, got %d", len(tools))
	}
	if tools[0].Name != "Bash" {
		t.Errorf("tool name = %q, want Bash", tools[0].Name)
	}
}
