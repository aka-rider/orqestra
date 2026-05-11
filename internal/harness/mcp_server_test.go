package harness

import (
	"encoding/json"
	"testing"
)

func TestHandleMCPRequest_Initialize(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	}
	resp := handleMCPRequest(req, "/tmp/test.sock")
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("missing capabilities")
	}
	if _, ok := caps["tools"]; !ok {
		t.Error("missing tools in capabilities")
	}
}

func TestHandleMCPRequest_ToolsList(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	}
	resp := handleMCPRequest(req, "/tmp/test.sock")
	if resp == nil {
		t.Fatal("expected response")
	}

	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "AskUserQuestion" {
		t.Errorf("tool name = %q, want AskUserQuestion", result.Tools[0].Name)
	}

	// Verify schema has 'question' as required
	var schema struct {
		Required []string `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(result.Tools[0].InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "question" {
		t.Errorf("required = %v, want [question]", schema.Required)
	}
	// Verify Claude Code compatible: question, options, allow_custom, multi_select
	for _, key := range []string{"question", "options", "allow_custom", "multi_select"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("missing property %q in schema", key)
		}
	}
}

func TestHandleMCPRequest_Notification(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	resp := handleMCPRequest(req, "/tmp/test.sock")
	if resp != nil {
		t.Error("expected nil for notification, got response")
	}
}

func TestHandleMCPRequest_UnknownMethod(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "bogus/method",
	}
	resp := handleMCPRequest(req, "/tmp/test.sock")
	if resp == nil {
		t.Fatal("expected error response")
	}
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
}

func TestFormatAnswer_Freeform(t *testing.T) {
	tc := MCPToolCall{Question: "What color?"}
	ans := MCPAnswer{FreeformText: "blue"}
	got := FormatAnswer(tc, ans)
	want := "User's answer: blue"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatAnswer_Skipped(t *testing.T) {
	tc := MCPToolCall{Question: "What color?"}
	ans := MCPAnswer{Skipped: true}
	got := FormatAnswer(tc, ans)
	if got == "" {
		t.Error("expected non-empty skip message")
	}
	if !contains(got, "skipped") {
		t.Errorf("expected skip message, got %q", got)
	}
}

func TestFormatAnswer_SingleSelect(t *testing.T) {
	tc := MCPToolCall{
		Question: "Pick one",
		Options: []MCPToolOption{
			{Label: "Alpha"},
			{Label: "Beta"},
		},
	}
	ans := MCPAnswer{SelectedIndices: []int{1}}
	got := FormatAnswer(tc, ans)
	want := "Selected: Beta"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatAnswer_SingleSelectWithCustom(t *testing.T) {
	tc := MCPToolCall{
		Question: "Pick one",
		Options: []MCPToolOption{
			{Label: "Alpha"},
			{Label: "Beta"},
		},
	}
	ans := MCPAnswer{
		SelectedIndices: []int{0},
		CustomTexts:     map[int]string{0: "with extra sauce"},
	}
	got := FormatAnswer(tc, ans)
	if !contains(got, "Selected: Alpha") || !contains(got, "Context: with extra sauce") {
		t.Errorf("unexpected format: %q", got)
	}
}

func TestFormatAnswer_MultiSelect(t *testing.T) {
	tc := MCPToolCall{
		Question:    "Pick many",
		MultiSelect: true,
		Options: []MCPToolOption{
			{Label: "A"},
			{Label: "B"},
			{Label: "C"},
		},
	}
	ans := MCPAnswer{SelectedIndices: []int{0, 2}}
	got := FormatAnswer(tc, ans)
	if !contains(got, "Selected (2 of 3)") {
		t.Errorf("unexpected header: %q", got)
	}
	if !contains(got, "- A") || !contains(got, "- C") {
		t.Errorf("missing selections: %q", got)
	}
}

func TestFormatAnswer_NoSelection(t *testing.T) {
	tc := MCPToolCall{
		Question: "Pick one",
		Options:  []MCPToolOption{{Label: "Alpha"}},
	}
	ans := MCPAnswer{}
	got := FormatAnswer(tc, ans)
	if !contains(got, "best judgment") {
		t.Errorf("expected fallback message, got %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
